package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	searchDefault  = 10
	searchMax      = 50
	getDefaultMax  = 200
	getMaxItems    = 5000
	appleAPIOrigin = "https://api.appstoreconnect.apple.com"
)

type searchIn struct {
	Query         string `json:"query" jsonschema:"keywords, type, tag or operation id"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max hits (default 10, max 50)"`
	IncludeWrites bool   `json:"include_writes,omitempty" jsonschema:"in read-only mode, also list mutations"`
}

type describeIn struct {
	OperationID string `json:"operation_id,omitempty" jsonschema:"e.g. apps_getCollection"`
	Method      string `json:"method,omitempty" jsonschema:"with path, instead of operation_id"`
	Path        string `json:"path,omitempty" jsonschema:"e.g. /v1/apps/123 or /v1/apps/{id}"`
}

type getIn struct {
	Path           string            `json:"path" jsonschema:"e.g. /v1/apps/123/builds"`
	Query          map[string]string `json:"query,omitempty" jsonschema:"e.g. {\"sort\":\"-createdDate\"}"`
	Fields         map[string]string `json:"fields,omitempty" jsonschema:"type → comma-separated fields"`
	Include        []string          `json:"include,omitempty"`
	Limit          int               `json:"limit,omitempty" jsonschema:"default 200, max 5000"`
	Cursor         string            `json:"cursor,omitempty" jsonschema:"next_cursor (same path)"`
	UnsafeUnlisted bool              `json:"unsafe_unlisted,omitempty" jsonschema:"allow a path not in the spec"`
}

type requestIn struct {
	tool.ConfirmFlag
	Method         string            `json:"method" jsonschema:"reads: asc_api_get"`
	Path           string            `json:"path" jsonschema:"e.g. /v1/appStoreVersions/123"`
	Query          map[string]string `json:"query,omitempty"`
	Body           map[string]any    `json:"body,omitempty" jsonschema:"JSON:API {data:{type,attributes,relationships}}"`
	UnsafeUnlisted bool              `json:"unsafe_unlisted,omitempty" jsonschema:"op not in the spec; always confirms"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "asc_api_search", Toolset: tool.Core, Class: tool.Read, Idempotent: true, Local: true,
		Title: "Search App Store Connect API",
		Description: "Find API operations by keywords, resource type or tag when no workflow tool fits; returns ranked " +
			"operation ids, method, path, summary. Then asc_api_describe.",
	}, apiSearch))
	tool.Register(tool.New(tool.Meta{
		Name: "asc_api_describe", Toolset: tool.Core, Class: tool.Read, Idempotent: true, Local: true,
		Title: "Describe API operation",
		Description: "Describe one API operation (operation_id or method+path): params, enums, fields[] names, includes, " +
			"body skeleton and a ready example call.",
	}, apiDescribe))
	tool.Register(tool.New(tool.Meta{
		Name: "asc_api_get", Toolset: tool.Core, Class: tool.Read, Idempotent: true,
		Title: "GET any API resource",
		Description: "GET any App Store Connect operation, validated against the pinned spec first (a mismatch returns the " +
			"skeleton, no call). Collections auto-paginate to limit with next_cursor.",
	}, apiGet))
	tool.Register(tool.New(tool.Meta{
		Name: "asc_api_request", Toolset: tool.Core, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Call any API mutation",
		Description: "POST/PATCH/DELETE any App Store Connect operation, spec-validated. Every call previews first and " +
			"needs confirm:true plus human approval. Prefer workflow tools; reads: asc_api_get.",
	}, apiRequest))
}

func apiSearch(_ context.Context, env *tool.Env, in searchIn) (*tool.Result, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = searchDefault
	}
	if limit > searchMax {
		return nil, fmt.Errorf("%w: limit must be at most %d", tool.ErrInvalidArgs, searchMax)
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("%w: query is empty", tool.ErrInvalidArgs)
	}
	readOnly := !env.Profile.AllowWrites && !in.IncludeWrites
	n := limit
	if readOnly {
		n = searchMax // over-fetch, then drop mutations
	}
	hits := []searchHit{}
	for _, h := range env.Spec.Search(in.Query, n) {
		if readOnly && h.Method != http.MethodGet {
			continue
		}
		if len(hits) < limit {
			hits = append(hits, searchHit{
				OperationID: h.ID, Method: h.Method, Path: h.Path, Summary: h.Summary,
				Tags: h.Tags, Deprecated: h.Deprecated, Score: h.Score,
			})
		}
	}
	res := tool.OK(map[string]any{"query": in.Query, "spec_version": env.Spec.Version(), "operations": hits})
	if readOnly {
		res.Warn("read-only mode: only GET operations listed; pass include_writes:true to see mutations")
	}
	if len(hits) > 0 {
		res.Then(tool.NextStep{Tool: "asc_api_describe", Args: map[string]any{"operation_id": hits[0].OperationID}})
	}
	return res, nil
}

// searchHit is one asc_api_search result; operation_id is the argument
// asc_api_describe takes.
type searchHit struct {
	OperationID string   `json:"operation_id"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Score       float64  `json:"score"`
}

func apiDescribe(_ context.Context, env *tool.Env, in describeIn) (*tool.Result, error) {
	id := in.OperationID
	switch {
	case id != "" && (in.Method != "" || in.Path != ""):
		return nil, fmt.Errorf("%w: pass operation_id or method+path, not both", tool.ErrInvalidArgs)
	case id == "" && (in.Method == "" || in.Path == ""):
		return nil, fmt.Errorf("%w: pass operation_id, or both method and path", tool.ErrInvalidArgs)
	case id == "":
		path, _, err := splitPath(in.Path)
		if err != nil {
			return nil, err
		}
		op, ok := env.Spec.Match(strings.ToUpper(in.Method), path)
		if !ok {
			return nil, tool.WithNext(
				fmt.Errorf("%w: no operation matches %s %s", tool.ErrInvalidArgs, strings.ToUpper(in.Method), path),
				tool.NextStep{Tool: "asc_api_search", Args: map[string]any{"query": path}})
		}
		id = op.ID
	}
	d, err := env.Spec.Describe(id)
	if err != nil {
		return nil, tool.WithNext(err, tool.NextStep{Tool: "asc_api_search", Args: map[string]any{"query": id}})
	}
	return tool.OK(d), nil
}

func apiGet(ctx context.Context, env *tool.Env, in getIn) (*tool.Result, error) {
	maxItems := in.Limit
	if maxItems <= 0 {
		maxItems = getDefaultMax
	}
	if maxItems > getMaxItems {
		return nil, tool.InvalidArgs("limit must be at most %d", getMaxItems)
	}
	path, q, err := splitPath(in.Path)
	if err != nil {
		return nil, err
	}
	if path, err = resolveAppPath(ctx, env, path); err != nil {
		return nil, err
	}
	if err := mergeQuery(q, in.Query); err != nil {
		return nil, err
	}
	for typ, fields := range in.Fields {
		if err := setQuery(q, "fields["+typ+"]", fields); err != nil {
			return nil, err
		}
	}
	if len(in.Include) > 0 {
		if err := setQuery(q, "include", strings.Join(in.Include, ",")); err != nil {
			return nil, err
		}
	}
	req := tool.Request{Method: http.MethodGet, Path: path, Query: q}
	op, listed := env.Spec.Match(http.MethodGet, path)
	if listed {
		if res := validation(env.Spec.Validate(req)); res != nil {
			return res, nil
		}
	} else if !in.UnsafeUnlisted {
		return unlisted(env, req), nil
	}

	out := map[string]any{"operation": op.ID}
	if !listed {
		out["operation"] = "unlisted"
	}
	if listed && isCollection(op.ID) {
		res, err := env.ASC.List(ctx, path, tool.ListOptions{Query: q, MaxItems: maxItems, Cursor: in.Cursor})
		if err != nil {
			return nil, err
		}
		out["data"], out["returned"] = res.Data, res.Returned
		if res.Included != nil {
			out["included"] = res.Included
		}
		if res.Total > 0 {
			out["total"] = res.Total
		}
		if res.NextCursor != "" {
			out["next_cursor"] = res.NextCursor
		}
		return tool.OK(out), nil
	}
	if in.Cursor != "" {
		return nil, fmt.Errorf("%w: cursor applies only to collection operations", tool.ErrInvalidArgs)
	}
	resp, err := env.ASC.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	addDocument(out, resp)
	return tool.OK(out), nil
}

func apiRequest(ctx context.Context, env *tool.Env, in requestIn) (*tool.Result, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
	case http.MethodGet, http.MethodHead:
		return nil, tool.WithNext(
			fmt.Errorf("%w: asc_api_request is for mutations; use asc_api_get for %s", tool.ErrInvalidArgs, method),
			tool.NextStep{Tool: "asc_api_get", Args: map[string]any{"path": in.Path}})
	default:
		return nil, fmt.Errorf("%w: method must be POST, PATCH or DELETE", tool.ErrInvalidArgs)
	}
	path, q, err := splitPath(in.Path)
	if err != nil {
		return nil, err
	}
	if err := mergeQuery(q, in.Query); err != nil {
		return nil, err
	}
	req := tool.Request{Method: method, Path: path, Query: q, Write: true}
	if in.Body != nil {
		req.Body = in.Body
	}

	op, listed := env.Spec.Match(method, path)
	sensitive := true
	opName := "unlisted"
	switch {
	case listed:
		if res := validation(env.Spec.Validate(req)); res != nil {
			return res, nil
		}
		sensitive = method == http.MethodDelete || env.Spec.Sensitive(method, path) || touchesPublicAttrs(in.Body)
		opName = op.ID
	case !in.UnsafeUnlisted:
		return unlisted(env, req), nil
	}

	// Every generic mutation needs a reviewed preview and a human: the spec
	// cannot tell which writes are public, paid or irreversible.
	wctx, gate := tool.RequireConfirm(ctx, env, in.Confirm, requestPreview(method, path, opName, q, in.Body, listed, sensitive))
	if gate != nil {
		return gate, nil
	}
	resp, err := env.ASC.Do(wctx, req)
	if method == http.MethodDelete && errors.Is(err, tool.ErrAlreadyGone) {
		// A replayed DELETE found the object gone: the first attempt applied.
		return tool.Already(map[string]any{"operation": opName, "method": method, "path": path}), nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"operation": opName, "method": method, "path": path, "status": resp.Status}
	addDocument(out, resp)
	return tool.OK(out), nil
}

func requestPreview(method, path, opName string, q url.Values, body map[string]any, listed, sensitive bool) tool.PlanPreview {
	intended := map[string]any{"method": method, "path": path, "operation": opName}
	if len(q) > 0 {
		intended["query"] = q.Encode()
	}
	if body != nil {
		intended["body"] = body
	}
	var irreversible []string
	switch {
	case method == http.MethodDelete:
		irreversible = []string{"deletes the object; App Store Connect has no undo"}
	case sensitive:
		irreversible = []string{"destructive or public action (review, release, pricing, people or external endpoints)"}
	}
	if !listed {
		irreversible = append(irreversible, "operation is not in the pinned spec; app-store-connect-mcp could not validate it")
	}
	return tool.PlanPreview{
		Summary:      fmt.Sprintf("%s %s (%s)", method, path, opName),
		Intended:     intended,
		Affected:     []string{path},
		Irreversible: irreversible,
	}
}

// unlisted refuses a request whose operation is not in the pinned spec.
func unlisted(env *tool.Env, req tool.Request) *tool.Result {
	res := validation(env.Spec.Validate(req))
	if res == nil {
		res = validation(&tool.ValidationError{Problems: []string{fmt.Sprintf("no operation matches %s %s", req.Method, req.Path)}})
	}
	if body, ok := res.Data.(map[string]any); ok {
		body["hint"] = "find the operation with asc_api_search; if Apple added it after the pinned spec, retry with unsafe_unlisted:true"
	}
	return res
}

// validation converts a spec validation failure into an error result that
// keeps the problems and skeleton; nil means valid.
func validation(err error) *tool.Result {
	if err == nil {
		return nil
	}
	var ve *tool.ValidationError
	if !errors.As(err, &ve) {
		return tool.Fail(err)
	}
	problems := ve.Problems
	body := map[string]any{
		"error":   tool.ErrInvalidArgs.Code,
		"message": ve.Error(),
		"hint":    "fix the request to match the skeleton, or find the right operation with asc_api_search",
	}
	if len(problems) > maxProblems {
		body["problems_omitted"] = len(problems) - maxProblems
		problems = problems[:maxProblems]
	}
	body["problems"] = problems
	if ve.Skeleton != nil {
		body["skeleton"] = ve.Skeleton
	}
	res := &tool.Result{Data: body, IsError: true}
	if ve.Skeleton == nil {
		res.Next = []tool.NextStep{{Tool: "asc_api_search"}}
	}
	return res
}

// publicAttrs are attributes whose change is public, paid or redirects data
// even on otherwise plain update endpoints.
var publicAttrs = []string{"subscriptionStatusUrl", "publicLinkEnabled", "familySharable", "releaseType", "promotionalText"}

// touchesPublicAttrs reports whether a JSON:API body sets one of publicAttrs
// (prefix match, so subscriptionStatusUrlVersion and ...ForSandbox count).
func touchesPublicAttrs(body map[string]any) bool {
	data, _ := body["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	for k := range attrs {
		for _, p := range publicAttrs {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
	}
	return false
}

// maxProblems caps the validation problems returned to the agent.
const maxProblems = 5

// resolveAppPath lets the app id segment of /v1/apps/{id}... be any app
// reference (bundle id, SKU, name, or "{app}" for the pinned app); Apple's
// app ids are numeric, so anything else is resolved.
func resolveAppPath(ctx context.Context, env *tool.Env, path string) (string, error) {
	segs := strings.Split(path, "/")
	if len(segs) < 4 || segs[2] != "apps" || isDigits(segs[3]) {
		return path, nil
	}
	ref, err := url.PathUnescape(segs[3])
	if err != nil {
		return "", fmt.Errorf("%w: bad app segment %q", tool.ErrInvalidArgs, segs[3])
	}
	if ref == "{app}" {
		ref = ""
	}
	app, err := env.Resolve.App(ctx, tool.AppRef(ref))
	if err != nil {
		return "", err
	}
	segs[3] = app.ID
	return strings.Join(segs, "/"), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isCollection reports whether a GET operation returns a list, per Apple's
// operation id naming.
func isCollection(opID string) bool {
	return strings.HasSuffix(opID, "_getCollection") ||
		strings.HasSuffix(opID, "_getToManyRelated") ||
		strings.HasSuffix(opID, "_getToManyRelationship")
}

// addDocument puts a decoded response into out: the single resource or the
// list, included resources, or the raw JSON body for non-JSON:API payloads.
func addDocument(out map[string]any, resp *tool.Response) {
	switch {
	case resp.Collection:
		out["data"] = resp.Data
	case len(resp.Data) == 1:
		out["data"] = resp.Data[0]
	case len(resp.Body) > 0 && json.Valid(resp.Body):
		out["raw"] = tool.Untrusted(resp.Body)
	}
	if len(resp.Included) > 0 {
		out["included"] = resp.Included
	}
	if next := resp.Links["next"]; next != "" && resp.Collection {
		out["has_more"] = true
	}
}

// splitPath validates an API path (optionally a full api.appstoreconnect
// URL) and splits off its query string. The path part must be plain: no
// percent escapes (an encoded %2e%2e or %2f would match a spec template yet
// reach Apple as another resource) and no dot segments.
func splitPath(raw string) (string, url.Values, error) {
	p := strings.TrimSpace(raw)
	p = strings.TrimPrefix(p, appleAPIOrigin)
	if !strings.HasPrefix(p, "/v") || strings.Contains(p, "://") || strings.Contains(p, "..") {
		return "", nil, fmt.Errorf("%w: path must be an App Store Connect API path such as /v1/apps", tool.ErrInvalidArgs)
	}
	path, rawQuery, _ := strings.Cut(p, "?")
	if strings.Contains(path, "%") {
		return "", nil, fmt.Errorf("%w: path must not contain percent escapes; pass ids as plain text", tool.ErrInvalidArgs)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "." {
			return "", nil, fmt.Errorf("%w: path has a dot segment: %s", tool.ErrInvalidArgs, path)
		}
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, fmt.Errorf("%w: query string: %v", tool.ErrInvalidArgs, err)
	}
	return path, q, nil
}

func mergeQuery(q url.Values, extra map[string]string) error {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := setQuery(q, k, extra[k]); err != nil {
			return err
		}
	}
	return nil
}

func setQuery(q url.Values, k, v string) error {
	if _, dup := q[k]; dup {
		return fmt.Errorf("%w: query parameter %q given twice", tool.ErrInvalidArgs, k)
	}
	q.Set(k, v)
	return nil
}
