package tool

import (
	"errors"
	"fmt"
	"strings"
)

// Error is a typed hamn error with a stable machine-readable code. The
// package-level Err* values are sentinels: wrap them with fmt.Errorf("...: %w")
// and test with errors.Is.
type Error struct {
	Code    string
	Message string
	Hint    string
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// Sentinel errors shared by every tool.
var (
	ErrWritesDisabled = &Error{
		Code:    "writes_disabled",
		Message: "writes are disabled",
		Hint:    "restart hamn with --allow-writes, HAMN_ALLOW_WRITES=1 or allow_writes=true in the profile",
	}
	ErrIndividualKeyUnsupported = &Error{
		Code:    "individual_key_unsupported",
		Message: "this operation is not available with an Individual API key",
		Hint:    "use a Team API key (profile with issuer_id)",
	}
	ErrNeedsConfirm = &Error{
		Code:    "needs_confirm",
		Message: "confirmation required",
		Hint:    "review the preview, then call again with confirm",
	}
	ErrStalePlan = &Error{
		Code:    "stale_plan",
		Message: "plan is unknown, expired, or the remote state changed since it was made",
		Hint:    "call again without confirm to get a fresh plan",
	}
	ErrOutcomeUnknown = &Error{
		Code:    "outcome_unknown",
		Message: "the request may or may not have been applied",
		Hint:    "read the object back before retrying; hamn never replays mutations",
	}
	ErrDeclined = &Error{
		Code:    "declined",
		Message: "the user declined the confirmation",
	}
	ErrInvalidArgs = &Error{
		Code:    "invalid_arguments",
		Message: "invalid arguments",
	}
	ErrRateLimited = &Error{
		Code:    "rate_limited",
		Message: "App Store Connect rate limit reached",
		Hint:    "wait for the hourly quota to recover; see doctor for remaining calls",
	}
	ErrInternal = &Error{
		Code:    "internal_error",
		Message: "internal error",
		Hint:    "this is a hamn bug; please report it",
	}
	ErrUnavailable = &Error{
		Code:    "unavailable",
		Message: "capability unavailable in this session",
	}
	ErrElicitationUnavailable = &Error{
		Code:    "elicitation_unavailable",
		Message: "the MCP client does not support elicitation",
	}
	ErrHumanRequired = &Error{
		Code:    "human_approval_required",
		Message: "this action needs explicit human approval, but the client cannot ask",
		Hint:    "use a client with elicitation support, or set allow_no_elicit=true in the profile",
	}
)

// APIError is a decoded App Store Connect error (JSON:API errors[] entry plus
// the HTTP status) enriched with a hint for the agent.
type APIError struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`
	Title   string `json:"title,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Hint    string `json:"hint,omitempty"`
	// More holds additional errors[] entries beyond the first, if any.
	More []APIError `json:"more,omitempty"`
}

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "App Store Connect %d", e.Status)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	} else if e.Title != "" {
		b.WriteString(": " + e.Title)
	}
	if e.Pointer != "" {
		b.WriteString(" (at " + e.Pointer + ")")
	}
	return b.String()
}

// ErrorBody is the structured payload of an error result.
type ErrorBody struct {
	Error      string      `json:"error"`
	Code       string      `json:"code,omitempty"`
	Hint       string      `json:"hint,omitempty"`
	Message    string      `json:"message"`
	API        *APIError   `json:"api,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Next       []NextStep  `json:"next,omitempty"`
}

// NextStep is a machine-readable follow-up suggestion: call Tool with Args.
// When Tool is hidden in the session (writes disabled) the server sets Locked
// and Unlock, the call that makes Tool available.
type NextStep struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Locked string         `json:"locked,omitempty"`
	Unlock *NextStep      `json:"unlock,omitempty"`
}

// NextStepper is implemented by errors that suggest follow-up calls.
type NextStepper interface {
	NextSteps() []NextStep
}

type nextError struct {
	err  error
	next []NextStep
}

func (e *nextError) Error() string         { return e.err.Error() }
func (e *nextError) Unwrap() error         { return e.err }
func (e *nextError) NextSteps() []NextStep { return e.next }

// WithNext attaches follow-up suggestions to err (nil stays nil).
func WithNext(err error, next ...NextStep) error {
	if err == nil {
		return nil
	}
	return &nextError{err: err, next: next}
}

// BodyFor converts any error into the structured error payload.
func BodyFor(err error) ErrorBody {
	b := ErrorBody{Error: "error", Message: err.Error()}
	var api *APIError
	if errors.As(err, &api) {
		b.Error = "api_error"
		b.Code = api.Code
		b.Hint = api.Hint
		b.API = api
	}
	var te *Error
	if errors.As(err, &te) {
		b.Error = te.Code
		if b.Hint == "" {
			b.Hint = te.Hint
		}
	}
	var nf *ErrNotFound
	if errors.As(err, &nf) {
		b.Error = "not_found"
		b.Hint = nf.Hint
	}
	var amb *ErrAmbiguous
	if errors.As(err, &amb) {
		b.Error = "ambiguous"
		b.Candidates = amb.Candidates
	}
	var ns NextStepper
	if errors.As(err, &ns) {
		b.Next = ns.NextSteps()
	}
	return b
}
