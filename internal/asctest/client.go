package asctest

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/spec"
)

// TestToken is the bearer token the client from [Server.NewClient] sends.
const TestToken = "asctest-token"

// ClientOption adjusts the [asc.Options] used by [Server.NewClient].
type ClientOption func(*asc.Options)

// WithWrites lets the client send non-GET requests (they still need a write
// context and Request.Write).
func WithWrites() ClientOption { return func(o *asc.Options) { o.AllowWrites = true } }

// WithOptions applies fn to the client options, e.g. to set MaxRetries.
func WithOptions(fn func(*asc.Options)) ClientOption { return ClientOption(fn) }

// NewClient returns an *asc.Client pointed at the fake, read-only unless
// [WithWrites] is given. It uses a static token, the spec index for hints
// and untrusted wrapping, and a clock whose Sleep returns at once so retry
// backoff does not slow tests.
func (s *Server) NewClient(t testing.TB, opts ...ClientOption) *asc.Client {
	t.Helper()
	base, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("asctest: server URL: %v", err)
	}
	idx, err := spec.Load()
	if err != nil {
		t.Fatalf("asctest: load spec: %v", err)
	}
	o := asc.Options{
		BaseURL:   base,
		Tokens:    staticTokens{},
		HTTP:      s.Client(),
		Spec:      idx,
		FreeText:  idx.FreeTextAttributes,
		Clock:     instantClock{},
		UserAgent: "hamn/asctest",
		Jitter:    func() float64 { return 0 },
	}
	for _, opt := range opts {
		opt(&o)
	}
	c, err := asc.New(o)
	if err != nil {
		t.Fatalf("asctest: new client: %v", err)
	}
	return c
}

// staticTokens is an asc.TokenSource returning [TestToken].
type staticTokens struct{}

// Token implements asc.TokenSource.
func (staticTokens) Token(context.Context) (string, error) { return TestToken, nil }

// Invalidate implements asc.TokenSource.
func (staticTokens) Invalidate() {}

// instantClock is a tool.Clock whose Sleep does not wait.
type instantClock struct{}

// Now implements tool.Clock.
func (instantClock) Now() time.Time { return time.Now() }

// Sleep implements tool.Clock; it only honours cancellation.
func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
