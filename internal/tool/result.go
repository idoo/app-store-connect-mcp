package tool

import "encoding/json"

// Image is binary image content returned alongside structured data.
type Image struct {
	MIMEType string
	Data     []byte
}

// Truncation describes what the response byte cap dropped.
type Truncation struct {
	Dropped int    `json:"dropped"`
	Hint    string `json:"hint"`
}

// Approval sources reported as _meta.hamn.approved_by.
const (
	// ApprovedHuman means the human accepted an elicitation for this call.
	ApprovedHuman = "human"
	// ApprovedAgent means the agent confirmed (confirm:true or plan_id)
	// without a human answer.
	ApprovedAgent = "agent"
	// ApprovedNone means no approval was given (reads, previews, ungated writes).
	ApprovedNone = "none"
)

// ResultMeta is reported under _meta.hamn on every result.
type ResultMeta struct {
	Profile   string      `json:"profile,omitempty"`
	PinSource string      `json:"pin_source,omitempty"`
	RateLimit *RateLimit  `json:"rate_limit,omitempty"`
	Truncated *Truncation `json:"truncated,omitempty"`
	Warnings  []string    `json:"warnings,omitempty"`
	// ApprovedBy is one of ApprovedHuman, ApprovedAgent, ApprovedNone.
	ApprovedBy string `json:"approved_by,omitempty"`
}

// Result is a tool outcome. The server renders Data as structuredContent and
// as compact JSON text (or Text when set), appends Images, applies redaction
// and the byte cap, and reports Meta and Next under _meta.hamn.
type Result struct {
	// Data is the structured payload; it should marshal to a JSON object.
	Data any
	// Text overrides the text content (default: compact JSON of Data).
	Text    string
	Images  []Image
	IsError bool
	// Next lists machine-readable follow-up calls the agent may make.
	Next []NextStep
	Meta ResultMeta
}

// OK returns a successful result carrying v.
func OK(v any) *Result { return &Result{Data: v} }

// WithImage appends an image to r and returns r.
func (r *Result) WithImage(mime string, data []byte) *Result {
	r.Images = append(r.Images, Image{MIMEType: mime, Data: data})
	return r
}

// Warn appends a warning to r's metadata and returns r.
func (r *Result) Warn(msg string) *Result {
	r.Meta.Warnings = append(r.Meta.Warnings, msg)
	return r
}

// Then appends follow-up suggestions to r and returns r.
func (r *Result) Then(next ...NextStep) *Result {
	r.Next = append(r.Next, next...)
	return r
}

// Fail returns an error result with the structured [ErrorBody] of err; the
// error's NextSteps (if any) are copied to Result.Next.
func Fail(err error) *Result {
	b := BodyFor(err)
	return &Result{Data: b, IsError: true, Next: b.Next}
}

// Already returns a successful result for an idempotent no-op: the same
// object v would produce on first success, plus "already":true. Non-object
// values are wrapped as {"already":true,"result":v}.
func Already(v any) *Result {
	b, err := json.Marshal(v)
	if err != nil || len(b) < 2 || b[0] != '{' {
		return OK(map[string]any{"already": true, "result": v})
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(b, &probe) != nil {
		return OK(map[string]any{"already": true, "result": v})
	}
	if _, dup := probe["already"]; dup || len(probe) == 0 {
		probe["already"] = json.RawMessage("true")
		out, _ := json.Marshal(probe)
		return OK(json.RawMessage(out))
	}
	out := append([]byte(`{"already":true,`), b[1:]...)
	return OK(json.RawMessage(out))
}

// StillProcessing returns the result of a wait that timed out
// ({"status":"still_processing","resume_hint":..,"result":v}).
func StillProcessing(v any, resumeHint string) *Result {
	return OK(map[string]any{"status": "still_processing", "resume_hint": resumeHint, "result": v})
}

// Untrusted wraps text written by third parties (review bodies, tester
// feedback, reviewer notes). It marshals as {"untrusted": text} so agents
// treat it as data, never as instructions.
type Untrusted string

// MarshalJSON implements json.Marshaler.
func (u Untrusted) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"untrusted": string(u)})
}
