package asc

import (
	"context"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Client defaults.
const (
	// DefaultBaseURL is the App Store Connect API root.
	DefaultBaseURL = "https://api.appstoreconnect.apple.com"
	// DefaultMaxRetries bounds retries of one request (429, transient errors).
	DefaultMaxRetries = 3
	// DefaultMaxConcurrent bounds the requests in flight per client.
	DefaultMaxConcurrent = 4
	// DefaultRequestTimeout bounds one HTTP exchange of the default HTTP client.
	DefaultRequestTimeout = 30 * time.Second
	// MaxBodyBytes caps any response body read by the client.
	MaxBodyBytes = 64 << 20
	// DefaultUserAgent is sent when Options.UserAgent is empty.
	DefaultUserAgent = "app-store-connect-mcp"
)

// TokenSource mints bearer JWTs. *auth.TokenSource satisfies it.
type TokenSource interface {
	// Token returns a valid JWT, refreshing it when needed.
	Token(ctx context.Context) (string, error)
	// Invalidate drops the cached JWT if it is still tok; the next Token call
	// then signs a new one.
	Invalidate(tok string)
}

// SpecHints is the slice of the spec index the client uses to enrich errors.
// *spec.Index satisfies it.
type SpecHints interface {
	// Match resolves a concrete method+path to its operation.
	Match(method, path string) (tool.Operation, bool)
	// EnumLookup returns the allowed values at an error source (JSON pointer
	// or query parameter name) of operation opID, or nil.
	EnumLookup(opID, pointer string) []string
}

// Options configures [New]. Only Tokens is required.
type Options struct {
	// BaseURL defaults to [DefaultBaseURL]. It must be https, or http on a
	// loopback host (tests).
	BaseURL *url.URL
	// Tokens supplies the bearer JWT.
	Tokens TokenSource
	// HTTP defaults to a client with [DefaultRequestTimeout].
	HTTP *http.Client
	// MaxRetries is the retry budget per request: 0 means
	// [DefaultMaxRetries], a negative value disables retries.
	MaxRetries int
	// MaxConcurrent defaults to [DefaultMaxConcurrent].
	MaxConcurrent int
	// AllowWrites is the initial state of the read-only guard (see
	// [Client.SetAllowWrites]); non-GET requests also need a write context
	// from tool.Call and Request.Write.
	AllowWrites bool
	// Spec enriches error hints with valid enum values; optional.
	Spec SpecHints
	// FreeText returns the free-text attributes of a resource type for
	// [tool.WrapUntrusted], which always adds [tool.UntrustedFallback].
	FreeText func(resourceType string) []string
	// Clock defaults to the system clock.
	Clock tool.Clock
	// UserAgent defaults to [DefaultUserAgent]; use "app-store-connect-mcp/<version>".
	UserAgent string
	// Jitter returns a value in [0,1) scaling the backoff jitter; defaults
	// to math/rand/v2.
	Jitter func() float64
}

// defaultJitter is the production jitter source.
func defaultJitter() float64 { return rand.Float64() } //nolint:gosec // jitter needs no cryptographic randomness
