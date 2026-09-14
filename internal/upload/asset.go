package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// Wait bounds for polling an asset or build until Apple finishes processing.
const (
	DefaultPollInterval = 2 * time.Second
	DefaultMaxWait      = 45 * time.Second
	MaxMaxWait          = 300 * time.Second
)

// Asset delivery states (AppMediaAssetState.state).
const (
	StateAwaitingUpload = "AWAITING_UPLOAD"
	StateUploadComplete = "UPLOAD_COMPLETE"
	StateComplete       = "COMPLETE"
	StateFailed         = "FAILED"
)

// DefaultStateField is the delivery-state attribute of most media assets
// (AppMediaAssetState).
const DefaultStateField = "assetDeliveryState"

// ErrUploadFailed is wrapped by every [*DeliveryError].
var ErrUploadFailed = &tool.Error{
	Code:    "upload_failed",
	Message: "App Store Connect rejected the uploaded file",
	Hint:    "fix the reported errors, delete the failed object and upload again",
}

// Deps are the collaborators of an upload. ASC requests are mutations
// (Request.Write): the caller's context must carry the write capability.
type Deps struct {
	ASC tool.ASC
	// Fetch sends the presigned part requests; it never adds Authorization.
	Fetch tool.Fetcher
	// Clock drives retry backoff and polling (nil: the system clock).
	Clock tool.Clock
	// State persists build-upload resume records (nil disables resume).
	State tool.StateStore
	// Progress receives cumulative bytes sent (optional).
	Progress func(done, total int64)
}

// DepsFromEnv builds Deps from a tool environment, reporting byte progress
// through env.Progress.
func DepsFromEnv(env *tool.Env) Deps {
	d := Deps{ASC: env.ASC, Fetch: env.Fetch, Clock: env.Clock, State: env.State}
	if p := env.Progress; p != nil {
		d.Progress = func(done, total int64) { p(float64(done), float64(total), "uploading") }
	}
	return d
}

func (d Deps) clock() tool.Clock {
	if d.Clock == nil {
		return tool.SystemClock{}
	}
	return d.Clock
}

// Relationship is the to-one parent an asset is reserved under.
type Relationship struct {
	// Name is the relationship name, e.g. "appScreenshotSet".
	Name string
	// Type is the parent resource type, e.g. "appScreenshotSets".
	Type string
	ID   string
}

// PollOptions bounds waiting for Apple's processing after commit.
type PollOptions struct {
	// Interval between polls (default [DefaultPollInterval]).
	Interval time.Duration
	// MaxWait is the total wait (default [DefaultMaxWait], at most
	// [MaxMaxWait]). A negative value skips polling.
	MaxWait time.Duration
}

func (o PollOptions) normalized() PollOptions {
	if o.Interval <= 0 {
		o.Interval = DefaultPollInterval
	}
	switch {
	case o.MaxWait == 0:
		o.MaxWait = DefaultMaxWait
	case o.MaxWait > MaxMaxWait:
		o.MaxWait = MaxMaxWait
	}
	return o
}

// StateDetail is one error, warning or info entry of a processing state.
type StateDetail struct {
	Code        string `json:"code,omitempty"`
	Description string `json:"description,omitempty"`
}

// DeliveryError reports that Apple finished processing with FAILED.
type DeliveryError struct {
	Type   string
	ID     string
	Errors []StateDetail
}

// Error implements error.
func (e *DeliveryError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: processing FAILED", e.Type, e.ID)
	for i, d := range e.Errors {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(strings.TrimPrefix(d.Code+": "+d.Description, ": "))
	}
	return b.String()
}

// Unwrap returns [ErrUploadFailed].
func (e *DeliveryError) Unwrap() error { return ErrUploadFailed }

// AssetSpec describes one media asset upload. The asset flow covers
// appScreenshots, appPreviews, inAppPurchaseAppStoreReviewScreenshots,
// subscriptionAppStoreReviewScreenshots and the v2 inAppPurchaseImages and
// subscriptionImages: reserve, PUT parts, commit with sourceFileChecksum,
// then poll StateField (assetDeliveryState unless set).
type AssetSpec struct {
	// CreatePath is the collection the reservation is POSTed to, e.g.
	// "/v1/appScreenshots"; the asset itself lives at CreatePath/{id}.
	CreatePath string
	// Type is the resource type, e.g. "appScreenshots".
	Type   string
	Parent Relationship
	// FileName is the base name reported to Apple.
	FileName string
	FileSize int64
	File     io.ReaderAt
	// Attributes adds reservation attributes (e.g. previewFrameTimeCode,
	// mimeType for appPreviews).
	Attributes map[string]any
	// CommitAttributes adds commit attributes (e.g. previewFrameTimeCode).
	CommitAttributes map[string]any
	// StateField is the delivery-state attribute polled after commit
	// (default [DefaultStateField]); appPreviews use "videoDeliveryState",
	// whose state also passes through PROCESSING.
	StateField string
	Poll       PollOptions
}

// AssetResult is the outcome of an asset upload.
type AssetResult struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	State    string        `json:"state"`
	Checksum string        `json:"source_file_checksum"`
	Errors   []StateDetail `json:"errors,omitempty"`
	Warnings []StateDetail `json:"warnings,omitempty"`
	// StillProcessing is true when the wait ended before a terminal state.
	StillProcessing bool `json:"still_processing,omitempty"`
}

// mediaState is the AppMediaAssetState schema.
type mediaState struct {
	State    string        `json:"state"`
	Errors   []StateDetail `json:"errors"`
	Warnings []StateDetail `json:"warnings"`
}

// Asset runs the asset flow. When Apple reports FAILED it returns the
// result together with a [*DeliveryError]; when the wait ends first the
// result has StillProcessing set and no error.
func Asset(ctx context.Context, d Deps, s AssetSpec) (*AssetResult, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	sum, err := FileMD5(io.NewSectionReader(s.File, 0, s.FileSize))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", s.Type, s.FileName, err)
	}
	attrs := map[string]any{"fileName": s.FileName, "fileSize": s.FileSize}
	for k, v := range s.Attributes {
		attrs[k] = v
	}
	resp, err := d.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: s.CreatePath, Write: true,
		Body: resourceBody(s.Type, "", attrs, map[string]Relationship{s.Parent.Name: s.Parent}),
	})
	if err != nil {
		return nil, fmt.Errorf("reserve %s %s: %w", s.Type, s.FileName, err)
	}
	res, ok := resp.First()
	if !ok || res.ID == "" {
		return nil, fmt.Errorf("reserve %s %s: response has no resource", s.Type, s.FileName)
	}
	out := &AssetResult{ID: res.ID, Type: s.Type, State: StateAwaitingUpload, Checksum: sum}
	reserved := func(err error) error {
		return fmt.Errorf("%s %s reserved but not completed (delete it or upload again): %w", s.Type, res.ID, err)
	}
	ops, err := ParseOperations(res)
	if err != nil {
		return nil, reserved(err)
	}
	if err := PutParts(ctx, d.Fetch, d.clock(), s.File, s.FileSize, ops, d.Progress); err != nil {
		return nil, reserved(err)
	}
	commit := map[string]any{"uploaded": true, "sourceFileChecksum": sum}
	for k, v := range s.CommitAttributes {
		commit[k] = v
	}
	itemPath := s.CreatePath + "/" + res.ID
	resp, err = d.ASC.Do(ctx, tool.Request{
		Method: http.MethodPatch, Path: itemPath, Write: true,
		Body: resourceBody(s.Type, res.ID, commit, nil),
	})
	if err != nil {
		return nil, reserved(fmt.Errorf("commit: %w", err))
	}
	if r, ok := resp.First(); ok {
		res = r
	}
	still, err := poll(ctx, d, itemPath, s.Poll, res, func(r tool.Resource) (bool, error) {
		st, err := assetState(r, s.stateField())
		if err != nil {
			return false, err
		}
		out.State, out.Errors, out.Warnings = st.State, st.Errors, st.Warnings
		return st.State == StateComplete || st.State == StateFailed, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s %s: poll: %w", s.Type, res.ID, err)
	}
	out.StillProcessing = still
	if out.State == StateFailed {
		return out, &DeliveryError{Type: s.Type, ID: res.ID, Errors: out.Errors}
	}
	return out, nil
}

func (s AssetSpec) check() error {
	switch {
	case !strings.HasPrefix(s.CreatePath, "/v") || strings.ContainsAny(s.CreatePath, "?#"):
		return fmt.Errorf("%w: asset create path %q", tool.ErrInvalidArgs, s.CreatePath)
	case s.Type == "":
		return fmt.Errorf("%w: asset type is empty", tool.ErrInvalidArgs)
	case s.Parent.Name == "" || s.Parent.Type == "" || s.Parent.ID == "":
		return fmt.Errorf("%w: %s needs a parent relationship", tool.ErrInvalidArgs, s.Type)
	case s.FileName == "" || strings.ContainsAny(s.FileName, `/\`):
		return fmt.Errorf("%w: file name %q", tool.ErrInvalidArgs, s.FileName)
	case s.FileSize <= 0 || s.File == nil:
		return fmt.Errorf("%w: %s is empty", tool.ErrInvalidArgs, s.FileName)
	}
	return nil
}

// stateField returns the polled delivery-state attribute.
func (s AssetSpec) stateField() string {
	if s.StateField == "" {
		return DefaultStateField
	}
	return s.StateField
}

func assetState(r tool.Resource, field string) (mediaState, error) {
	var st mediaState
	if err := decodeAttr(r, field, &st); err != nil {
		return st, err
	}
	if st.State == "" {
		st.State = StateAwaitingUpload
	}
	return st, nil
}

// poll re-reads path until terminal reports true or the wait ends, reporting
// whether processing is still under way. first is the resource already in
// hand (checked before the first GET).
func poll(ctx context.Context, d Deps, path string, o PollOptions, first tool.Resource, terminal func(tool.Resource) (bool, error)) (bool, error) {
	o = o.normalized()
	if done, err := terminal(first); err != nil || done {
		return false, err
	}
	if o.MaxWait < 0 {
		return true, nil
	}
	clock := d.clock()
	deadline := clock.Now().Add(o.MaxWait)
	for {
		wait := min(o.Interval, deadline.Sub(clock.Now()))
		if wait <= 0 {
			return true, nil
		}
		if err := clock.Sleep(ctx, wait); err != nil {
			return false, err
		}
		resp, err := d.ASC.Get(ctx, path, nil)
		if err != nil {
			return false, err
		}
		r, ok := resp.First()
		if !ok {
			return false, fmt.Errorf("GET %s: empty response", path)
		}
		if done, err := terminal(r); err != nil || done {
			return false, err
		}
	}
}

// resourceBody builds a JSON:API {"data":{...}} request document.
func resourceBody(typ, id string, attrs map[string]any, rels map[string]Relationship) map[string]any {
	data := map[string]any{"type": typ}
	if id != "" {
		data["id"] = id
	}
	if len(attrs) > 0 {
		data["attributes"] = attrs
	}
	if len(rels) > 0 {
		rm := make(map[string]any, len(rels))
		for name, r := range rels {
			rm[name] = map[string]any{"data": map[string]string{"type": r.Type, "id": r.ID}}
		}
		data["relationships"] = rm
	}
	return map[string]any{"data": data}
}

// decodeAttr re-decodes a flattened attribute into v; a missing or null
// attribute leaves v unchanged.
func decodeAttr(r tool.Resource, key string, v any) error {
	raw, ok := r.Attributes[key]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(tool.Unwrap(raw))
	if err != nil {
		return fmt.Errorf("%s %s: encode %s: %w", r.Type, r.ID, key, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s %s: decode %s: %w", r.Type, r.ID, key, err)
	}
	return nil
}
