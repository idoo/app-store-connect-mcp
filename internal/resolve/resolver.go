// Package resolve implements [tool.Resolver]: it turns the references people
// use ("Hjul", "latest", "German", "External testers") into App Store Connect
// ids using only the [tool.ASC] interface. Failures are always
// *tool.ErrNotFound or *tool.ErrAmbiguous carrying follow-up suggestions.
package resolve

import (
	"sync"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// cacheTTL bounds how long the app list and per-app platform sets are reused.
const cacheTTL = 5 * time.Minute

// maxCandidates caps the candidates reported by an ambiguous reference.
const maxCandidates = 5

// Options configures a Resolver.
type Options struct {
	// DefaultApp is the profile's (or pin's) app reference used when a tool
	// receives an empty AppRef. It may be an id, bundle id, SKU or name.
	DefaultApp string
	// Clock drives cache expiry; nil means the system clock.
	Clock tool.Clock
}

// Resolver is the App Store Connect backed [tool.Resolver]. It is safe for
// concurrent use; lists that rarely change are cached for five minutes.
type Resolver struct {
	asc        tool.ASC
	defaultApp string
	clock      tool.Clock

	mu        sync.Mutex
	apps      []tool.App
	appsAt    time.Time
	platforms map[string]platformEntry
}

type platformEntry struct {
	list []tool.Platform
	at   time.Time
}

var _ tool.Resolver = (*Resolver)(nil)

// New returns a Resolver that issues its lookups through asc.
func New(asc tool.ASC, opts Options) *Resolver {
	clock := opts.Clock
	if clock == nil {
		clock = tool.SystemClock{}
	}
	return &Resolver{
		asc:        asc,
		defaultApp: opts.DefaultApp,
		clock:      clock,
		platforms:  map[string]platformEntry{},
	}
}

// fresh reports whether a cache entry stamped at is still valid.
func (r *Resolver) fresh(at time.Time) bool {
	return !at.IsZero() && r.clock.Now().Sub(at) < cacheTTL
}

// next builds a one-element NextStep list.
func next(toolName string, args map[string]any) []tool.NextStep {
	return []tool.NextStep{{Tool: toolName, Args: args}}
}
