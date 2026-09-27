package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/idoo/hamn/internal/ascrules"
	"github.com/idoo/hamn/internal/tool"
)

// applyConcurrency is the number of localization requests in flight.
const applyConcurrency = 4

// Outcome is the result of one applied change.
type Outcome string

// Apply outcomes.
const (
	OutcomeOK      Outcome = "ok"
	OutcomeAlready Outcome = "already"
	OutcomeError   Outcome = "error"
)

// Targets names the parents new localizations are created under.
type Targets struct {
	VersionID string
	AppInfoID string
}

// ItemResult reports one applied [Change].
type ItemResult struct {
	Locale  string   `json:"locale"`
	Level   Level    `json:"level"`
	Op      Op       `json:"op"`
	Fields  []string `json:"fields,omitempty"`
	ID      string   `json:"id,omitempty"`
	Outcome Outcome  `json:"outcome"`
	Error   string   `json:"error,omitempty"`
}

// levelAPI holds the per-level resource names.
type levelAPI struct {
	typ, parentRel, parentType, collection string
}

var apis = map[Level]levelAPI{
	LevelVersion: {typ: "appStoreVersionLocalizations", parentRel: "appStoreVersion", parentType: "appStoreVersions", collection: "/v1/appStoreVersions/%s/appStoreVersionLocalizations"},
	LevelAppInfo: {typ: "appInfoLocalizations", parentRel: "appInfo", parentType: "appInfos", collection: "/v1/appInfos/%s/appInfoLocalizations"},
}

// Apply executes plan, applyConcurrency requests at a time: PATCH for
// updates, POST for creates (a duplicate-locale 409 is read back and
// patched, reported as already), DELETE for removals (404 = already).
// ctx must carry the write capability. progress, when non-nil, is called
// after each change. Per-change failures are reported in the results; the
// returned error is only for an unusable plan.
func Apply(ctx context.Context, client tool.ASC, plan *Plan, ids Targets, progress func(done, total int)) ([]ItemResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("%w: apply needs a plan", tool.ErrInternal)
	}
	if len(plan.Errors) > 0 {
		return nil, fmt.Errorf("%w: plan has %d error(s)", ErrInvalidListing, len(plan.Errors))
	}
	for _, c := range plan.Changes {
		if c.Op == OpCreate && parentID(c.Level, ids) == "" {
			return nil, fmt.Errorf("%w: creating %s localization %s needs its parent id", tool.ErrInvalidArgs, c.Level, c.Locale)
		}
	}
	results := make([]ItemResult, len(plan.Changes))
	var (
		mu   sync.Mutex
		done int
	)
	// Per-change failures land in results and never cancel the others.
	perr := tool.Parallel(ctx, applyConcurrency, plan.Changes, func(ctx context.Context, i int, c Change) error {
		results[i] = applyOne(ctx, client, c, ids)
		if progress != nil {
			mu.Lock()
			done++
			progress(done, len(plan.Changes))
			mu.Unlock()
		}
		return nil
	})
	if perr != nil {
		for i, c := range plan.Changes {
			if results[i].Outcome == "" {
				results[i] = ItemResult{Locale: c.Locale, Level: c.Level, Op: c.Op, ID: c.ID, Outcome: OutcomeError, Error: "not sent: " + perr.Error()}
			}
		}
	}
	return results, nil
}

func parentID(level Level, ids Targets) string {
	if level == LevelAppInfo {
		return ids.AppInfoID
	}
	return ids.VersionID
}

func applyOne(ctx context.Context, client tool.ASC, c Change, ids Targets) ItemResult {
	r := ItemResult{Locale: c.Locale, Level: c.Level, Op: c.Op, ID: c.ID, Outcome: OutcomeOK}
	attrs := map[string]any{}
	for _, f := range c.Fields {
		r.Fields = append(r.Fields, f.Field)
		if f.New == "" && f.Field != "name" {
			attrs[f.Field] = nil // the spec's clear is null; "" fails URL validation
		} else {
			attrs[f.Field] = f.New
		}
	}
	api := apis[c.Level]
	var err error
	switch c.Op {
	case OpUpdate:
		err = patch(ctx, client, api, c.ID, attrs)
	case OpDelete:
		_, err = client.Do(ctx, tool.Request{Method: http.MethodDelete, Path: "/v1/" + api.typ + "/" + c.ID, Write: true})
		var apiErr *tool.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			r.Outcome, err = OutcomeAlready, nil
		}
	case OpCreate:
		r.ID, r.Outcome, err = create(ctx, client, api, c.Locale, parentID(c.Level, ids), attrs)
	default:
		err = fmt.Errorf("%w: unknown op %q", tool.ErrInternal, c.Op)
	}
	if err != nil {
		r.Outcome, r.Error = OutcomeError, err.Error()
	}
	return r
}

func patch(ctx context.Context, client tool.ASC, api levelAPI, id string, attrs map[string]any) error {
	_, err := client.Do(ctx, tool.Request{
		Method: http.MethodPatch, Path: "/v1/" + api.typ + "/" + id, Write: true,
		Body: map[string]any{"data": map[string]any{"type": api.typ, "id": id, "attributes": attrs}},
	})
	return err
}

func create(ctx context.Context, client tool.ASC, api levelAPI, locale, parent string, attrs map[string]any) (string, Outcome, error) {
	body := map[string]any{"locale": locale}
	for k, v := range attrs {
		body[k] = v
	}
	req := tool.Request{
		Method: http.MethodPost, Path: "/v1/" + api.typ, Write: true,
		Body: map[string]any{"data": map[string]any{
			"type": api.typ, "attributes": body,
			"relationships": map[string]any{api.parentRel: map[string]any{"data": map[string]any{"type": api.parentType, "id": parent}}},
		}},
	}
	res, err := client.Do(ctx, req)
	if err == nil {
		r, _ := res.First()
		return r.ID, OutcomeOK, nil
	}
	if !ascrules.IsAlready(req, err) {
		return "", OutcomeError, err
	}
	// Read back the existing localization and bring it to the desired state.
	found, rerr := client.Get(ctx, fmt.Sprintf(api.collection, parent), url.Values{"filter[locale]": {locale}})
	if rerr != nil {
		return "", OutcomeError, fmt.Errorf("%w (read-back failed: %v)", err, rerr)
	}
	for _, r := range found.Data {
		if l, _ := attrString(r.Attributes["locale"]); l == locale {
			if len(attrs) > 0 {
				if perr := patch(ctx, client, api, r.ID, attrs); perr != nil {
					return r.ID, OutcomeError, perr
				}
			}
			return r.ID, OutcomeAlready, nil
		}
	}
	return "", OutcomeError, err
}
