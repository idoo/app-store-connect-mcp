package asc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Retry timing.
const (
	baseBackoff = 500 * time.Millisecond
	// maxRetryAfter caps an honoured Retry-After; longer waits fail fast.
	maxRetryAfter = 30 * time.Second
	// jitterFraction is the maximum backoff jitter (25%).
	jitterFraction = 0.25
	// consistencyRetries and consistencyWait bound the eventual-consistency
	// 404 retries of GETs flagged with [WithEventualConsistency].
	consistencyRetries = 3
	consistencyWait    = time.Second
)

type consistencyKey struct{}

// WithEventualConsistency marks ctx so GETs answered 404 are retried
// (3 times, 1 s apart): use it to read an object right after creating it
// (new versions, buildUploads), which Apple may not serve immediately.
func WithEventualConsistency(ctx context.Context) context.Context {
	return context.WithValue(ctx, consistencyKey{}, true)
}

// eventualConsistency reports whether ctx carries [WithEventualConsistency].
func eventualConsistency(ctx context.Context) bool {
	v, _ := ctx.Value(consistencyKey{}).(bool)
	return v
}

// retryPolicy is the pure retry table of the client.
type retryPolicy struct {
	// max is the number of retries allowed after the first attempt.
	max int
	// jitter returns a value in [0,1).
	jitter func() float64
}

// shouldRetry decides, after attempt (0-based) failed with status or err,
// whether to try again and how long to wait first:
//   - 429 is retried for any method (Apple rejects it before applying);
//   - 408/500/502/503/504, timeouts and connection resets only for
//     GET/HEAD/PUT/DELETE, since a mutation may have been applied;
//   - a refused connection (nothing sent) for any method.
//
// The wait is exponential from 500 ms with up to 25% jitter; a Retry-After
// header is applied by the caller.
func (p retryPolicy) shouldRetry(method string, status int, err error, attempt int) (bool, time.Duration) {
	if attempt >= p.max {
		return false, 0
	}
	var retry bool
	switch {
	case err != nil && isConnRefused(err):
		retry = true
	case err != nil:
		retry = idempotent(method) && (isTimeout(err) || isConnReset(err))
	case status == http.StatusTooManyRequests:
		retry = true
	case status == http.StatusRequestTimeout, status == http.StatusInternalServerError,
		status == http.StatusBadGateway, status == http.StatusServiceUnavailable,
		status == http.StatusGatewayTimeout:
		retry = idempotent(method)
	}
	if !retry {
		return false, 0
	}
	return true, p.backoff(attempt)
}

// backoff returns 500 ms × 2^attempt plus up to 25% jitter.
func (p retryPolicy) backoff(attempt int) time.Duration {
	d := baseBackoff << min(attempt, 10)
	j := 0.0
	if p.jitter != nil {
		j = p.jitter()
	}
	return d + time.Duration(float64(d)*jitterFraction*j)
}

// idempotent reports whether method may be replayed after an unknown outcome.
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// parseRetryAfter parses a Retry-After header in seconds or as an HTTP-date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}

// isConnRefused reports a connection refused before the request was sent.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// isTimeout reports a network or client timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isConnReset reports a connection dropped mid-exchange.
func isConnReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}
