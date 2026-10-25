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

// NewClient returns a read-only *asc.Client pointed at the fake. It uses a
// static token, the spec index for hints and untrusted wrapping, and a clock
// whose Sleep returns at once so retry backoff does not slow tests.
func (s *Server) NewClient(t testing.TB) *asc.Client {
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
func (staticTokens) Invalidate(string) {}

// instantClock is a tool.Clock whose Sleep does not wait.
type instantClock struct{}

// Now implements tool.Clock.
func (instantClock) Now() time.Time { return time.Now() }

// Sleep implements tool.Clock; it only honours cancellation.
func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
