package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/ascrules"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// TestBackoffRetryAfter (CF2+CF5 for upload parts): Retry-After clamps
// before multiplying, beyond the cap fails fast, and ≤ 0 / past dates /
// garbage keep the exponential floor (never a 0 or negative wait).
func TestBackoffRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	over := ascrules.MaxRetryAfter + time.Second
	tests := []struct {
		header  string
		attempt int
		want    time.Duration
		ok      bool
	}{
		{"", 1, partBackoff, true},
		{"", 3, 4 * partBackoff, true},
		{"7", 1, 7 * time.Second, true},
		{"30", 2, 30 * time.Second, true},
		{"31", 1, over, false},
		{"9223372036854775807", 1, over, false},
		{"99999999999999999999999", 1, over, false},
		{now.Add(24 * time.Hour).Format(http.TimeFormat), 1, over, false},
		{now.Add(10 * time.Second).Format(http.TimeFormat), 1, 10 * time.Second, true},
		{"0", 1, partBackoff, true},
		{"0", 2, 2 * partBackoff, true},
		{"-5", 1, partBackoff, true},
		{"-9223372036854775808", 1, partBackoff, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 1, partBackoff, true},
		{"soon", 1, partBackoff, true},
	}
	for _, tt := range tests {
		last := &partStatusError{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {tt.header}}}
		got, ok := backoff(tt.attempt, last, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("backoff(%d, Retry-After %q) = %v, %v; want %v, %v", tt.attempt, tt.header, got, ok, tt.want, tt.ok)
		}
	}
	if got, ok := backoff(1, errors.New("reset"), now); got != partBackoff || !ok {
		t.Errorf("backoff(transport error) = %v, %v", got, ok)
	}
}

// statusFetcher answers every request with status and header.
type statusFetcher struct {
	status int
	header http.Header
	calls  atomic.Int32
}

func (f *statusFetcher) Fetch(_ context.Context, _, _ string, _ http.Header, _ io.Reader, _ int64) (*tool.FetchResponse, error) {
	f.calls.Add(1)
	return &tool.FetchResponse{Status: f.status, Header: f.header}, nil
}

// TestPutPartsRetryAfterOverCapFailsFast: an overflowing Retry-After stops
// the part with rate_limited after one attempt, without sleeping.
func TestPutPartsRetryAfterOverCapFailsFast(t *testing.T) {
	data := []byte("abcdef")
	f := &statusFetcher{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"9223372036854775807"}}}
	clock := tooltest.NewFakeClock()
	err := PutParts(context.Background(), f, clock, bytes.NewReader(data), int64(len(data)), splitOps(6, 1), nil)
	if !errors.Is(err, tool.ErrRateLimited) {
		t.Fatalf("err = %v, want rate_limited", err)
	}
	if f.calls.Load() != 1 || clock.Slept != 0 {
		t.Fatalf("calls = %d, slept = %v; want 1 call, no sleep", f.calls.Load(), clock.Slept)
	}
}

// TestPutPartsRetryAfterZeroSleepsFloor: Retry-After 0 still waits the
// exponential floor between attempts.
func TestPutPartsRetryAfterZeroSleepsFloor(t *testing.T) {
	data := []byte("abcdef")
	f := &statusFetcher{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"0"}}}
	clock := tooltest.NewFakeClock()
	if err := PutParts(context.Background(), f, clock, bytes.NewReader(data), int64(len(data)), splitOps(6, 1), nil); err == nil {
		t.Fatal("want error after retries")
	}
	if want := partBackoff * (1 + 2 + 4); clock.Slept != want || int(f.calls.Load()) != PartRetries+1 {
		t.Fatalf("slept = %v, calls = %d; want %v, %d", clock.Slept, f.calls.Load(), want, PartRetries+1)
	}
}
