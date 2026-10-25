package tool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Error is a typed app-store-connect-mcp error with a stable machine-readable code. The
// package-level Err* values are sentinels: wrap them with fmt.Errorf("...: %w")
// and test with errors.Is.
type Error struct {
	Code    string
	Message string
	Hint    string
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// EnableWritesHint is the one way app-store-connect-mcp tells agents to turn writes on: the
// toolsets tool asks the human in the client and, when the client cannot
// ask, returns the restart steps for that client.
const EnableWritesHint = `call toolsets {"action":"enable_writes"} so the user can approve writes for this session (if the client cannot show the approval, it returns the restart steps)`

// Sentinel errors shared by every tool.
var (
	ErrWritesDisabled = &Error{
		Code:    "writes_disabled",
		Message: "writes are disabled",
		Hint:    EnableWritesHint,
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
		Hint:    "re-run the planning call (release_status / *_plan, or this tool without plan_id/confirm) for a fresh plan",
	}
	ErrPreviewRequired = &Error{
		Code:    "preview_required",
		Message: "confirm:true needs a preview of the same arguments in this session first",
		Hint:    "call again without confirm, show the preview to the user, then repeat with the same arguments and confirm:true",
	}
	ErrOutcomeUnknown = &Error{
		Code:    "outcome_unknown",
		Message: "the request may or may not have been applied",
		Hint:    "read the object back before retrying; app-store-connect-mcp never replays mutations",
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
		Hint:    "this is a app-store-connect-mcp bug; please report it",
	}
	ErrUnavailable = &Error{
		Code:    "unavailable",
		Message: "capability unavailable in this session",
	}
	ErrElicitationUnavailable = &Error{
		Code:    "elicitation_unavailable",
		Message: "the MCP client does not support elicitation",
	}
	ErrCancelled = &Error{
		Code:    "cancelled",
		Message: "the request was cancelled",
	}
	ErrHumanRequired = &Error{
		Code:    "human_approval_required",
		Message: "this action needs explicit human approval, but the client cannot ask",
		Hint:    "use a client that supports elicitation; in CI set ASC_MCP_CI=1 and ASC_MCP_ALLOW_NO_ELICIT=1, or allow_no_elicit=true in the profile",
	}
	ErrAlreadyGone = &Error{
		Code:    "already_gone",
		Message: "the object no longer exists; a retried delete was already applied",
	}
	ErrResponseTooLarge = &Error{
		Code:    "response_too_large",
		Message: "the App Store Connect response is too large",
		Hint:    "narrow the request with fields[], filter[] or a smaller limit",
	}
	ErrUpstreamInvalid = &Error{
		Code:    "upstream_invalid",
		Message: "App Store Connect returned a response that is not valid JSON:API",
		Hint:    "a proxy or Apple outage may be in the way; check https://developer.apple.com/system-status/ and retry later",
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
// When Tool is hidden in the session the server sets Locked (why) and, when
// the session can change that, Unlock: the call that makes Tool available.
// Hint says what to do when there is no Unlock (stateless HTTP).
type NextStep struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Locked string         `json:"locked,omitempty"`
	Unlock *NextStep      `json:"unlock,omitempty"`
	Hint   string         `json:"hint,omitempty"`
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
		if errors.Is(err, ErrOutcomeUnknown) {
			te = ErrOutcomeUnknown // "may have been applied" dominates any inner cause
		}
		b.Error = te.Code
		if b.Hint == "" {
			b.Hint = te.Hint
		}
	} else if api == nil && errors.Is(err, context.Canceled) {
		b.Error = ErrCancelled.Code
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

// InvalidArgs returns an error wrapping [ErrInvalidArgs] with the formatted
// message ("invalid arguments: <msg>"). %w in format wraps as in fmt.Errorf.
func InvalidArgs(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidArgs}, args...)...)
}

// APIStatus returns the HTTP status of the [APIError] in err's chain, or 0.
func APIStatus(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// IsNotFound reports a reference that matched nothing: a resolver
// [ErrNotFound] or an App Store Connect 404.
func IsNotFound(err error) bool {
	var nf *ErrNotFound
	return errors.As(err, &nf) || APIStatus(err) == http.StatusNotFound
}

// IsConflict reports an App Store Connect 409 (e.g. the object already
// exists). Callers must read the state back before trusting it.
func IsConflict(err error) bool {
	return APIStatus(err) == http.StatusConflict
}
