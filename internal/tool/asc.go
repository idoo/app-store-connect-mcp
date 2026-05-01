package tool

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Request is one App Store Connect API call. Path carries its own version
// prefix (/v1, /v2, /v3) and may contain a query string only when Query is nil.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// Body is marshaled as JSON (application/json) when non-nil.
	Body any
	// RawBody is sent verbatim with ContentType for non-JSON:API calls; it
	// takes precedence over Body.
	RawBody     []byte
	ContentType string
	// Accept overrides the Accept header (e.g. "application/a-gzip" for reports).
	Accept string
	// Write must be true for any non-GET request; the client additionally
	// requires the context to carry [WithWrite].
	Write bool
}

// Response is a decoded App Store Connect response.
type Response struct {
	Status int
	Header http.Header
	// Data holds the flattened primary data. A single-resource document yields
	// one element and Collection=false.
	Data       []Resource
	Collection bool
	Included   []Resource
	Meta       map[string]any
	Links      map[string]string
	// Body is the raw (decompressed) response body.
	Body []byte
}

// First returns the first primary resource, if any.
func (r *Response) First() (Resource, bool) {
	if r == nil || len(r.Data) == 0 {
		return Resource{}, false
	}
	return r.Data[0], true
}

// ListOptions configures an auto-paginating GET of a collection.
type ListOptions struct {
	// Query holds any extra query parameters (filter[...], sort, exists[...]).
	Query url.Values
	// Fields maps a resource type to its sparse fieldset (fields[type]).
	Fields map[string][]string
	// Include lists relationship names to include.
	Include []string
	// Limit is the per-page size (default 200, Apple's maximum for most lists).
	Limit int
	// MaxItems caps the total number of primary items (default 200, max 5000).
	MaxItems int
	// Cursor resumes from a next_cursor returned by a previous list.
	Cursor string
}

// ListResult is the merged outcome of an auto-paginated list.
type ListResult struct {
	Data []Resource `json:"data"`
	// Included is deduplicated by type+id across pages.
	Included   []Resource `json:"included,omitempty"`
	Total      int        `json:"total,omitempty"`
	Returned   int        `json:"returned"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// RateLimit is the parsed X-Rate-Limit header of the latest response.
type RateLimit struct {
	Limit     int  `json:"limit"`
	Remaining int  `json:"remaining"`
	Known     bool `json:"-"`
}

// ASC is the App Store Connect client used by tools. The implementation lives
// in internal/asc; tests use tooltest.FakeASC. Non-2xx responses are returned
// as *APIError; GETs are retried, mutations never replayed (except 429).
type ASC interface {
	// Do performs one request and decodes the JSON:API document (if any).
	Do(ctx context.Context, req Request) (*Response, error)
	// Get is Do with method GET.
	Get(ctx context.Context, path string, query url.Values) (*Response, error)
	// List GETs a collection, following links.next up to opts.MaxItems.
	List(ctx context.Context, path string, opts ListOptions) (*ListResult, error)
	// Download performs an authenticated GET (req.Method is ignored) and
	// returns the raw body without JSON parsing or compaction, e.g. gzip
	// sales and finance reports. Non-2xx responses are *APIError.
	Download(ctx context.Context, req Request) ([]byte, http.Header, error)
	// RateLimit returns the most recent rate-limit information.
	RateLimit() RateLimit
	// ClockSkew returns local clock minus Apple's Date header, if observed.
	ClockSkew() (time.Duration, bool)
}
