package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestSource(t *testing.T) (*TokenSource, *fakeClock) {
	t.Helper()
	key, err := NewKey("KEY123", pemPKCS8(t, newP256(t)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(key, "issuer")
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{now: fixedNow}
	return NewTokenSource(s, WithClock(clk.Now)), clk
}

func (ts *TokenSource) signCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.signs
}

func TestTokenSourceRefreshMargin(t *testing.T) {
	// Token expires at fixedNow - 30s + 19m = fixedNow + 18m30s, so it must
	// be refreshed once less than 2 min remain: after fixedNow + 16m30s.
	tests := []struct {
		name      string
		advance   time.Duration
		wantSigns int
	}{
		{"fresh token reused", time.Minute, 1},
		{"exactly at margin reused", 16*time.Minute + 30*time.Second, 1},
		{"inside margin refreshed", 16*time.Minute + 31*time.Second, 2},
		{"expired refreshed", time.Hour, 2},
	}
	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, clk := newTestSource(t)
			first, err := ts.Token(ctx)
			if err != nil {
				t.Fatal(err)
			}
			clk.Advance(tc.advance)
			second, err := ts.Token(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := ts.signCount(); got != tc.wantSigns {
				t.Fatalf("signs = %d, want %d", got, tc.wantSigns)
			}
			if same := first == second; same != (tc.wantSigns == 1) {
				t.Fatalf("token reused = %t, want %t", same, tc.wantSigns == 1)
			}
		})
	}
}

func TestTokenSourceInvalidate(t *testing.T) {
	ts, _ := newTestSource(t)
	ctx := context.Background()
	if _, err := ts.Token(ctx); err != nil {
		t.Fatal(err)
	}
	ts.Invalidate()
	if _, err := ts.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ts.signCount(); got != 2 {
		t.Fatalf("signs after Invalidate = %d, want 2", got)
	}
}

func TestTokenSourceConcurrentSingleSign(t *testing.T) {
	ts, _ := newTestSource(t)
	const n = 64
	tokens := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { tokens[i], errs[i] = ts.Token(context.Background()) })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("caller %d got a different token", i)
		}
	}
	if got := ts.signCount(); got != 1 {
		t.Fatalf("signs = %d, want 1", got)
	}
}

func TestTokenSourceCanceledContext(t *testing.T) {
	ts, _ := newTestSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ts.Token(ctx); err == nil {
		t.Fatal("expected error for canceled context")
	}
	if got := ts.signCount(); got != 0 {
		t.Fatalf("signs = %d, want 0", got)
	}
}
