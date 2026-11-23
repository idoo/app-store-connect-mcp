package developer

import (
	"context"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/devsite"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	searchLimitDefault = 10
	searchLimitMax     = 30
	siteJS             = "site-js"
)

// sourceNote says where the catalog has o from and whether it ever answered
// on the live site.
func sourceNote(o devsite.Operation) string {
	var from []string
	for _, s := range o.Source {
		if s == siteJS {
			s = "the account site's own JavaScript"
		}
		from = append(from, s)
	}
	note := "from " + strings.Join(from, ", ")
	if o.Verified == "" {
		return note + "; not yet verified against the live site by this server"
	}
	return note + "; it answered as described on the live site on " + o.Verified
}

func init() {
	groups := make([]any, len(devsite.Groups))
	for i, g := range devsite.Groups {
		groups[i] = g
	}
	tool.Register(tool.New(tool.Meta{
		Name: "developer_api_search", Toolset: tool.Developer, Class: tool.Read, Idempotent: true, Local: true,
		Title: "Find a developer account operation",
		Description: "Find a developer account operation (keys, identifiers, certificates, devices, profiles, " +
			"capabilities, membership) by words in the catalog of known services-account calls; then developer_api_describe.",
		Enums:  map[string][]any{"group": groups},
		Ranges: map[string][2]int{"limit": {1, searchLimitMax}},
	}, apiSearch))
	tool.Register(tool.New(tool.Meta{
		Name: "developer_api_describe", Toolset: tool.Developer, Class: tool.Read, Idempotent: true, Local: true,
		Title: "Describe a developer account operation",
		Description: "One known developer account operation: method, path, params with known values, answer keys, " +
			"notes, and the ready developer_api_get or developer_api_request call. verified: the day it answered live, if any.",
		Enums: map[string][]any{"portal_platform": {"ios", "mac"}},
	}, apiDescribe))
}

type apiSearchIn struct {
	Query string `json:"query,omitempty" jsonschema:"words, e.g. revoke key; empty lists the group or all"`
	Group string `json:"group,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"default 10"`
}

type apiDescribeIn struct {
	Operation string `json:"operation" jsonschema:"id (keys.revoke), row ref (K6) or METHOD path"`
	Platform  string `json:"portal_platform,omitempty" jsonschema:"fills {platform} in the path; default ios"`
	ID        string `json:"id,omitempty" jsonschema:"fills {id}"`
}

type operationRow struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Group  string `json:"group"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Read   bool   `json:"read"`
	// Verified is the day the operation answered on the live site.
	Verified string `json:"verified,omitempty"`
}

func apiSearch(_ context.Context, _ *tool.Env, in apiSearchIn) (*tool.Result, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = searchLimitDefault
	}
	limit = min(limit, searchLimitMax)
	ops := devsite.Search(in.Query, in.Group, limit)
	rows := make([]operationRow, len(ops))
	for i, o := range ops {
		rows[i] = operationRow{ID: o.ID, Title: o.Title, Group: o.Group, Method: o.Method, Path: o.Path, Read: o.Read, Verified: o.Verified}
	}
	out := map[string]any{"count": len(rows), "operations": rows}
	if strings.TrimSpace(in.Query) == "" && in.Group == "" {
		out["groups"] = devsite.Groups
	}
	return tool.OK(out), nil
}

// described is an operation as developer_api_describe shows it; its
// verified is the catalog's date, empty for an operation never run live.
type described struct {
	devsite.Operation
	Call       describedCall `json:"call"`
	SourceNote string        `json:"source_note"`
}

type describedCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func apiDescribe(_ context.Context, _ *tool.Env, in apiDescribeIn) (*tool.Result, error) {
	o, ok := devsite.Find(in.Operation)
	if !ok {
		return nil, tool.WithNext(
			tool.InvalidArgs("no known operation %s", tool.SanitizeLine(in.Operation, 200)),
			tool.NextStep{Tool: "developer_api_search", Args: map[string]any{"query": in.Operation}})
	}
	name, args := o.Call(map[string]string{"platform": in.Platform, "id": in.ID})
	return tool.OK(described{
		Operation:  o,
		Call:       describedCall{Tool: name, Arguments: args},
		SourceNote: sourceNote(o),
	}), nil
}
