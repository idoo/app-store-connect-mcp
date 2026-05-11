package auth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenSource caches one signed token per profile and refreshes it when less
// than [RefreshMargin] of validity remains. It is safe for concurrent use;
// concurrent callers needing a refresh share a single signing operation.
type TokenSource struct {
	signer *Signer
	now    func() time.Time

	mu    sync.Mutex
	cur   Token
	signs int
}

// Option configures a TokenSource.
type Option func(*TokenSource)

// WithClock injects the time source (default time.Now).
func WithClock(now func() time.Time) Option {
	return func(ts *TokenSource) {
		if now != nil {
			ts.now = now
		}
	}
}

// NewTokenSource returns a caching TokenSource for signer.
func NewTokenSource(signer *Signer, opts ...Option) *TokenSource {
	ts := &TokenSource{signer: signer, now: time.Now}
	for _, o := range opts {
		o(ts)
	}
	return ts
}

// Token returns a valid bearer JWT, signing a new one when the cached token is
// missing, invalidated, or within [RefreshMargin] of expiry.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := ts.now()
	if ts.cur.Value != "" && ts.cur.ExpiresAt.Sub(now) >= RefreshMargin {
		return ts.cur.Value, nil
	}
	tok, err := ts.signer.Sign(now)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	ts.cur = tok
	ts.signs++
	return tok.Value, nil
}

// Invalidate drops the cached token so the next Token call signs a fresh one
// (used after Apple answers 401).
func (ts *TokenSource) Invalidate() {
	ts.mu.Lock()
	ts.cur = Token{}
	ts.mu.Unlock()
}

// String identifies the source without revealing the token.
func (ts *TokenSource) String() string { return "auth.TokenSource{" + ts.signer.String() + "}" }

// GoString redacts for %#v.
func (ts *TokenSource) GoString() string { return ts.String() }
