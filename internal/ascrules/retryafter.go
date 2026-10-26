package ascrules

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxRetryAfter caps an honoured Retry-After; longer waits fail fast.
const MaxRetryAfter = 30 * time.Second

// ParseRetryAfter parses a Retry-After header in seconds or as an HTTP-date.
// The value is clamped before converting, so huge values cannot overflow
// into a negative wait; anything beyond [MaxRetryAfter] comes back as
// MaxRetryAfter+1s so the caller fails fast. A wait ≤ 0 (0, a negative
// number, a past date) or an unparsable header reports false, leaving the
// caller's normal backoff floor in place.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	const over = MaxRetryAfter + time.Second
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		switch {
		case n <= 0:
			return 0, false
		case n > int64(MaxRetryAfter/time.Second):
			return over, true
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0, false
	}
	return min(d, over), true
}
