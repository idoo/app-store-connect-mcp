package tool

import "fmt"

// Operation identifies one OpenAPI operation of the pinned App Store Connect spec.
type Operation struct {
	ID         string   `json:"id"`
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Summary    string   `json:"summary,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
}

// SearchHit is a ranked search result.
type SearchHit struct {
	Operation
	Score float64 `json:"score"`
}

// Param is one operation parameter (path or query).
type Param struct {
	Name     string   `json:"name"`
	In       string   `json:"in"`
	Required bool     `json:"required,omitempty"`
	Type     string   `json:"type,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// OpDescription is the compact description returned by asc_api_describe.
type OpDescription struct {
	Operation
	Params []Param `json:"params,omitempty"`
	// BodyType is the request body's data.type, if the operation takes a body.
	BodyType string `json:"body_type,omitempty"`
	// BodySkeleton is a minimal valid body with required attributes and relationships.
	BodySkeleton any `json:"body_skeleton,omitempty"`
	// Example is a copy-pasteable asc_api_request argument object.
	Example any `json:"example,omitempty"`
	// Fields maps resource type to its selectable fields[] names.
	Fields map[string][]string `json:"fields,omitempty"`
	// Includes lists valid include= values.
	Includes []string `json:"includes,omitempty"`
	// Sensitive is true for operations treated as destructive.
	Sensitive bool `json:"sensitive,omitempty"`
}

// ValidationError reports why a request does not match the spec. Skeleton
// carries the describe output so the agent can correct the call.
type ValidationError struct {
	Problems []string       `json:"problems"`
	Skeleton *OpDescription `json:"skeleton,omitempty"`
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "request does not match the App Store Connect spec: " + e.Problems[0]
	}
	return fmt.Sprintf("request does not match the App Store Connect spec (%d problems)", len(e.Problems))
}

// Spec is the embedded App Store Connect OpenAPI index (internal/spec).
type Spec interface {
	// Version is the pinned spec version (e.g. "4.5.1").
	Version() string
	// Search ranks operations by keyword, tag or resource type.
	Search(query string, limit int) []SearchHit
	// Describe returns the compact description of an operation id.
	Describe(operationID string) (*OpDescription, error)
	// Match resolves a concrete method+path (e.g. GET /v1/apps/123) to its operation.
	Match(method, path string) (Operation, bool)
	// Validate checks a request against the spec; failures are *ValidationError.
	Validate(req Request) error
	// Sensitive reports whether method+path is on the hand-kept destructive list.
	Sensitive(method, path string) bool
}
