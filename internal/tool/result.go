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

// ResultMeta is reported under _meta.hamn on every result.
type ResultMeta struct {
	Profile   string      `json:"profile,omitempty"`
	PinSource string      `json:"pin_source,omitempty"`
	RateLimit *RateLimit  `json:"rate_limit,omitempty"`
	Truncated *Truncation `json:"truncated,omitempty"`
	Warnings  []string    `json:"warnings,omitempty"`
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

// Text returns a successful text-only result.
func Text(s string) *Result { return &Result{Text: s} }

// ImageResult returns a result with one image.
func ImageResult(mime string, data []byte) *Result {
	return &Result{Images: []Image{{MIMEType: mime, Data: data}}}
}

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

// Already returns a successful result for an idempotent no-op
// ({"ok":true,"already":true,"result":v}).
func Already(v any) *Result {
	return OK(map[string]any{"ok": true, "already": true, "result": v})
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
