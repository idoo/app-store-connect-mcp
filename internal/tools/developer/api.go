package developer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "developer_api_get", Toolset: tool.Developer, Class: tool.Read, Idempotent: true,
		Title: "Read any developer account API",
		Description: "Read any developer.apple.com services-account endpoint with the Apple ID sign-in (web_login): " +
			"v1 JSON:API GETs (query) or legacy reads (form or json). Answers are Apple's JSON as third-party data. " +
			"Writes and downloads: developer_api_request.",
	}, apiGet))
	tool.Register(tool.New(tool.Meta{
		Name: "developer_api_request", Toolset: tool.Developer, Class: tool.Destructive,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Call any developer account API write",
		Description: "Send one developer.apple.com services-account write or legacy download with the Apple ID sign-in " +
			"(web_login). Previews first; needs confirm:true and human approval. Downloads need save_to: the body goes " +
			"to a file, never to you. Reads: developer_api_get.",
		Enums: map[string][]any{"method": {"POST", "PATCH", "PUT", "DELETE", "GET"}},
	}, apiRequest))
}

type apiGetIn struct {
	Path   string         `json:"path" jsonschema:"relative to services-account/, e.g. v1/bundleIds or QH65B2/account/auth/key/list"`
	Query  map[string]any `json:"query,omitempty" jsonschema:"v1 read query; values a string or a list of strings"`
	Form   map[string]any `json:"form,omitempty" jsonschema:"legacy read's form fields"`
	JSON   map[string]any `json:"json,omitempty" jsonschema:"JSON read's body (legacy, or a v1 read the site sends as POST); teamId added when absent"`
	TeamID string         `json:"team_id,omitempty" jsonschema:"developer team id; default the API key's team"`
}

type apiRequestIn struct {
	tool.ConfirmFlag
	Method string         `json:"method" jsonschema:"GET only for a legacy download"`
	Path   string         `json:"path" jsonschema:"relative to services-account/"`
	Query  map[string]any `json:"query,omitempty" jsonschema:"v1 DELETE's or legacy download's query"`
	JSON   map[string]any `json:"json,omitempty" jsonschema:"JSON body, sent as given (v1: JSON:API with data.attributes.teamId)"`
	Form   map[string]any `json:"form,omitempty" jsonschema:"legacy form body; teamId added"`
	TeamID string         `json:"team_id,omitempty" jsonschema:"developer team id; default the API key's team"`
	SaveTo string         `json:"save_to,omitempty" jsonschema:"new file under the root for the answer's body (0600, not returned); required for download paths"`
}

// isV1 reports a JSON:API path.
func isV1(p string) bool { return strings.HasPrefix(p, "v1/") || strings.HasPrefix(p, "v2/") }

func apiGet(ctx context.Context, env *tool.Env, in apiGetIn) (*tool.Result, error) {
	path, err := cleanAPIPath(in.Path)
	if err != nil {
		return nil, err
	}
	// A v1 read is a GET with a query, or one of the few POSTs with a JSON
	// body the site itself only reads with; a legacy read is a POST.
	v1 := isV1(path)
	method := http.MethodPost
	if v1 && in.JSON == nil {
		method = http.MethodGet
	}
	if !tool.DevSiteIsRead(method, path) {
		return nil, tool.WithNext(
			tool.InvalidArgs("%s %s is not a read; use developer_api_request", method, path),
			tool.NextStep{Tool: "developer_api_request", Args: map[string]any{"method": http.MethodPost, "path": path}})
	}
	call := tool.DevSiteCall{Method: method, Path: path}
	switch {
	case in.Form != nil && in.JSON != nil:
		return nil, tool.InvalidArgs("pass form or json, not both")
	case v1 && in.Form != nil:
		return nil, tool.InvalidArgs("a v1 read takes query, or json for a read the site sends as POST; form is for legacy reads")
	case v1 && in.JSON != nil && in.Query != nil:
		return nil, tool.InvalidArgs("a v1 read with a json body takes no query")
	case !v1 && in.Query != nil:
		return nil, tool.InvalidArgs("a legacy read takes form or json; query is for v1 reads")
	}
	if call.Query, err = values("query", in.Query); err != nil {
		return nil, err
	}
	if call.Form, err = values("form", in.Form); err != nil {
		return nil, err
	}
	team, err := tool.ResolveDevTeam(ctx, env, in.TeamID)
	if err != nil {
		return nil, err
	}
	call.TeamID = team
	if in.JSON != nil {
		call.JSON, call.Form = withTeam(in.JSON, team, v1), nil
	}
	ans, err := env.DevSite.API(ctx, call)
	if err != nil {
		return nil, siteErr(err)
	}
	out := map[string]any{"status": ans.Status, "team_id": team}
	return answerResult(ctx, env, out, path, ans)
}

// withTeam is a copy of a JSON read's body carrying the team id where the
// site expects it, unless the caller put one there: at the top of a legacy
// body, in data.attributes of a v1 body.
func withTeam(in map[string]any, team string, v1 bool) map[string]any {
	body := maps.Clone(in)
	at := body
	if v1 {
		data, _ := body["data"].(map[string]any)
		attrs, _ := data["attributes"].(map[string]any)
		if attrs == nil {
			return body
		}
		data, attrs = maps.Clone(data), maps.Clone(attrs)
		data["attributes"], body["data"] = attrs, data
		at = attrs
	}
	if _, ok := at["teamId"]; !ok {
		at["teamId"] = team
	}
	return body
}

// answerResult adds the decoded answer to out as data (JSON) or text,
// storing an oversize one as an artifact.
func answerResult(ctx context.Context, env *tool.Env, out map[string]any, path string, ans tool.DevSiteAnswer) (*tool.Result, error) {
	data, text, isJSON := decodeAnswer(ans)
	var b []byte
	if isJSON {
		out["data"] = data
		var err error
		if b, err = json.Marshal(data); err != nil {
			return nil, fmt.Errorf("%w: encode answer: %v", tool.ErrInternal, err)
		}
	} else {
		out["text"] = tool.Untrusted(text)
		if ans.ContentType != "" {
			out["content_type"] = ans.ContentType
		}
		b = []byte(text)
	}
	if len(b) <= inlineMax {
		return tool.OK(out), nil
	}
	delete(out, "data")
	delete(out, "text")
	mime, name := "text/plain", "developer-"+slug(path)+".txt"
	if isJSON {
		out["data_summary"] = summarize(data, len(b))
		mime, name = "application/json", "developer-"+slug(path)+".json"
	}
	hint := fmt.Sprintf("the answer is %d bytes, too large to inline", len(b))
	if env.Artifacts == nil {
		out["data_hint"] = hint + "; narrow the query (fields, filters, limit)"
		return tool.OK(out), nil
	}
	link, err := env.Artifacts.Put(ctx, name, mime, b)
	if err != nil {
		out["data_hint"] = hint + " and could not be stored: " + tool.BodyFor(err).Message + "; narrow the query"
		return tool.OK(out), nil
	}
	out["data_resource"] = link.URI
	out["data_hint"] = hint + "; read data_resource for the whole answer, or narrow the query"
	return tool.OK(out).WithLink(link), nil
}

// summarize describes a decoded answer without its values.
func summarize(v any, size int) map[string]any {
	s := map[string]any{"bytes": size}
	switch x := v.(type) {
	case map[string]any:
		keys := map[string]any{}
		for k, e := range x {
			keys[k] = kind(e)
		}
		s["keys"] = keys
	case []any:
		s["items"] = len(x)
	}
	return s
}

func kind(v any) any {
	switch x := v.(type) {
	case []any:
		return map[string]int{"items": len(x)}
	case map[string]any:
		return map[string]int{"keys": len(x)}
	case nil:
		return "null"
	case string, tool.Untrusted:
		return "string"
	case bool:
		return "bool"
	}
	return "number"
}

// isDownload reports a path whose last segment starts with "download": its
// answer may be key material.
func isDownload(path string) bool {
	last := path[strings.LastIndexByte(path, '/')+1:]
	return strings.HasPrefix(strings.ToLower(last), "download")
}

func apiRequest(ctx context.Context, env *tool.Env, in apiRequestIn) (*tool.Result, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	path, err := cleanAPIPath(in.Path)
	if err != nil {
		return nil, err
	}
	v1 := isV1(path)
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
	case http.MethodGet:
		if v1 {
			return nil, readRefused(method, path)
		}
	default:
		return nil, tool.InvalidArgs("method must be POST, PATCH, PUT, DELETE, or GET for a legacy download")
	}
	if tool.DevSiteIsRead(method, path) {
		return nil, readRefused(method, path)
	}
	switch {
	case in.JSON != nil && in.Form != nil:
		return nil, tool.InvalidArgs("pass json or form, not both")
	case v1 && in.Form != nil:
		return nil, tool.InvalidArgs("v1 paths take json, not form")
	case v1 && method == http.MethodDelete && in.JSON != nil:
		return nil, tool.InvalidArgs("a v1 DELETE takes query only")
	case !v1 && method == http.MethodGet && (in.JSON != nil || in.Form != nil):
		return nil, tool.InvalidArgs("a legacy GET takes query only")
	case in.Query != nil && method != http.MethodDelete && (v1 || method != http.MethodGet):
		return nil, tool.InvalidArgs("query applies to a v1 DELETE or a legacy GET only")
	}
	call := tool.DevSiteCall{Method: method, Path: path}
	if call.Query, err = values("query", in.Query); err != nil {
		return nil, err
	}
	if call.Form, err = values("form", in.Form); err != nil {
		return nil, err
	}
	if in.JSON != nil {
		call.JSON, call.Form = in.JSON, nil
	}
	saveTo := strings.TrimSpace(in.SaveTo)
	switch {
	case saveTo == "" && isDownload(path):
		return nil, tool.InvalidArgs("%s answers with key material: pass save_to, a new file under the root; its body is never returned", path)
	case saveTo != "":
		if _, err := env.Files.Resolve(saveTo); err != nil {
			return nil, tool.InvalidArgs("save_to: %s", tool.BodyFor(err).Message)
		}
		if _, err := env.Files.Stat(saveTo); err == nil {
			return nil, tool.InvalidArgs("save_to already exists; name a new file")
		}
	}
	team, err := tool.ResolveDevTeam(ctx, env, in.TeamID)
	if err != nil {
		return nil, err
	}
	call.TeamID = team

	wctx, gate := tool.RequireConfirm(ctx, env, in.Confirm, requestPreview(call, saveTo))
	if gate != nil {
		return gate, nil
	}
	ans, err := env.DevSite.API(wctx, call)
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		r := tool.OK(map[string]any{
			"outcome": "outcome_unknown", "method": method, "path": path, "team_id": team,
			"hint": "Apple may or may not have applied it; read the state back with developer_api_get before anything else. It was not resent.",
		})
		return r.Warn(tool.BodyFor(err).Message), nil
	}
	if err != nil {
		return nil, siteErr(err)
	}
	out := map[string]any{"status": ans.Status, "outcome": "done", "method": method, "path": path, "team_id": team}
	if saveTo != "" {
		savedAs, err := writeNew(env.Files, saveTo, ans.Body)
		if err != nil {
			return nil, fmt.Errorf("the answer (status %d) could not be saved and is not shown: %w", ans.Status, err)
		}
		saved := map[string]any{"path": savedAs, "bytes": len(ans.Body)}
		if savedAs != saveTo {
			saved["note"] = "a file appeared at save_to during the call; the answer was saved beside it, nothing was replaced"
		}
		if ans.ContentType != "" {
			saved["content_type"] = ans.ContentType
		}
		out["saved"] = saved
		return tool.OK(out), nil
	}
	return answerResult(ctx, env, out, path, ans)
}

// writeNew saves data to a new file (0600) at path. A download exists once,
// so when a file appeared at path since it was checked the data goes to
// path.1 … path.9 instead: it is neither dropped nor written over a file.
// The name written is returned.
func writeNew(files tool.Files, path string, data []byte) (string, error) {
	name := path
	err := files.WriteNewFile(name, data, 0o600)
	for i := 1; errors.Is(err, fs.ErrExist) && i <= 9; i++ {
		name = path + "." + strconv.Itoa(i)
		err = files.WriteNewFile(name, data, 0o600)
	}
	return name, err
}

func readRefused(method, path string) error {
	return tool.WithNext(
		tool.InvalidArgs("%s %s only reads; use developer_api_get", method, path),
		tool.NextStep{Tool: "developer_api_get", Args: map[string]any{"path": path}})
}

// requestPreview shows the call as it is sent: method, path, team, the
// whole query and body, and where a download goes. Nothing is shortened; a
// prompt too large to review is refused by the approval gate.
func requestPreview(c tool.DevSiteCall, saveTo string) tool.PlanPreview {
	intended := map[string]any{"method": c.Method, "path": c.Path, "team_id": c.TeamID}
	if len(c.Query) > 0 {
		intended["query"] = c.Query.Encode()
	}
	switch {
	case c.JSON != nil:
		intended["json"] = c.JSON
	case len(c.Form) > 0:
		intended["form"] = c.Form
	}
	p := tool.PlanPreview{
		Summary:  fmt.Sprintf("%s developer.apple.com/services-account/%s (team %s)", c.Method, c.Path, c.TeamID),
		Intended: intended,
		Affected: []string{c.Method + " " + siteOrigin + apiPrefix + c.Path},
	}
	if c.Method == http.MethodDelete {
		p.Irreversible = append(p.Irreversible, "deletes on the developer account; Apple has no undo")
	}
	if saveTo != "" {
		intended["save_to"] = saveTo
		p.Warnings = append(p.Warnings, "the answer's body is written to save_to (mode 0600) and not shown")
	}
	if isDownload(c.Path) {
		p.Irreversible = append(p.Irreversible, "Apple may let key material be downloaded only once")
	}
	return p
}
