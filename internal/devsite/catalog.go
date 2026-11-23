// Package devsite is the catalog of the Apple Developer account site's
// known services-account operations, as open-source clients (fastlane,
// asc-cli, expo) call them and as the site's own JavaScript does
// (source "site-js", refs W…). An operation's Verified is the day this
// project last saw it answer live; empty means never.
package devsite

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"unicode"
)

//go:embed operations.json
var operationsJSON []byte

// Groups are the operation groups, in catalog order.
var Groups = []string{
	"session", "team", "account", "membership", "keys", "services_ids", "pass_type_ids", "website_push_ids", "merchant_ids",
	"cloud_containers", "app_groups", "app_ids", "certificates", "devices", "profiles", "capabilities", "bundle_ids",
	"identifiers", "licenses", "service_usage",
}

// Wire values: how an operation goes to the site.
const (
	WireV1       = "v1"       // JSON:API under v1/
	WireForm     = "form"     // legacy form-encoded POST
	WireJSON     = "json"     // legacy JSON POST
	WireDownload = "download" // legacy GET whose body is a file
)

// Value is one known value of a parameter.
type Value struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Param is one parameter of an operation. The team id is never listed: the
// tools add it, except a v1 write's data.attributes.teamId.
type Param struct {
	Name string `json:"name"`
	// In is path, query, form or json; a json name is dotted
	// ("data.attributes.name").
	In       string  `json:"in"`
	Required bool    `json:"required"`
	Note     string  `json:"note,omitempty"`
	Values   []Value `json:"values,omitempty"`
}

// Operation is one known call of the developer account site.
type Operation struct {
	ID string `json:"id"`
	// Ref is the row of the reference the operation comes from ("K1").
	Ref   string `json:"ref"`
	Group string `json:"group"`
	Title string `json:"title"`
	// Method is what tool.DevSiteCall.Method takes.
	Method string `json:"method"`
	// Path is relative to services-account/, with {platform} and {id}.
	Path string `json:"path"`
	Wire string `json:"wire"`
	// Read is tool.DevSiteIsRead of Method and Path.
	Read   bool     `json:"read"`
	Params []Param  `json:"params"`
	Answer []string `json:"answer"`
	Notes  []string `json:"notes"`
	// Status is observed, observed_live_by_source or inferred.
	Status string   `json:"status"`
	Source []string `json:"source"`
	// Verified says when this project verified the operation live; empty
	// means never.
	Verified string `json:"verified"`
}

var load = sync.OnceValue(func() []Operation {
	var ops []Operation
	if err := json.Unmarshal(operationsJSON, &ops); err != nil {
		panic("devsite: operations.json: " + err.Error())
	}
	return ops
})

// All returns every operation in file order.
func All() []Operation { return slices.Clone(load()) }

// Find returns the operation with the id or the reference row ref names, or
// the one whose template matches "METHOD path" or a bare path ({...}
// segments match any one segment). Of several matches the one with the
// most literal segments wins, then the first in file order.
func Find(ref string) (Operation, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Operation{}, false
	}
	ops := load()
	for _, o := range ops {
		if o.ID == ref {
			return o, true
		}
	}
	for _, o := range ops {
		if strings.EqualFold(o.Ref, ref) {
			return o, true
		}
	}
	method, path := "", ref
	if m, p, ok := strings.Cut(ref, " "); ok {
		method, path = strings.ToUpper(m), strings.TrimSpace(p)
	}
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimPrefix(path, "https://developer.apple.com/")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "services-account/")
	segs := strings.Split(path, "/")
	best, bestLiteral := -1, -1
	for i, o := range ops {
		if method != "" && o.Method != method {
			continue
		}
		if n, ok := matchTemplate(o.Path, segs); ok && n > bestLiteral {
			best, bestLiteral = i, n
		}
	}
	if best < 0 {
		return Operation{}, false
	}
	return ops[best], true
}

// matchTemplate reports whether segs fit the template and how many of its
// segments are literal.
func matchTemplate(template string, segs []string) (literal int, ok bool) {
	want := strings.Split(template, "/")
	if len(want) != len(segs) {
		return 0, false
	}
	for i, w := range want {
		switch {
		case w == segs[i]:
			literal++
		case isPlaceholder(w) && segs[i] != "":
		default:
			return 0, false
		}
	}
	return literal, true
}

func isPlaceholder(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

// Search returns the operations of group (all when empty) that match the
// query's words, best first: an operation scores two per word found in its
// id or title and one per word found in its group, path or notes; ties
// keep file order. An empty query lists the group. limit <= 0 is no limit.
func Search(query, group string, limit int) []Operation {
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	type hit struct {
		op    Operation
		score int
	}
	var hits []hit
	for _, o := range load() {
		if group != "" && o.Group != group {
			continue
		}
		score := 0
		if len(tokens) > 0 {
			strong := strings.ToLower(o.ID + " " + o.Title)
			weak := strings.ToLower(o.Group + " " + o.Path + " " + strings.Join(o.Notes, " "))
			for _, t := range tokens {
				switch {
				case strings.Contains(strong, t):
					score += 2
				case strings.Contains(weak, t):
					score++
				}
			}
			if score == 0 {
				continue
			}
		}
		hits = append(hits, hit{o, score})
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return b.score - a.score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Operation, len(hits))
	for i, h := range hits {
		out[i] = h.op
	}
	return out
}

// FilledPath is Path with {platform} replaced by values["platform"] (ios
// when absent) and {id} by values["id"] (kept when absent).
func (o Operation) FilledPath(values map[string]string) string {
	platform := values["platform"]
	if platform == "" {
		platform = "ios"
	}
	p := strings.ReplaceAll(o.Path, "{platform}", platform)
	if id := values["id"]; id != "" {
		p = strings.ReplaceAll(p, "{id}", id)
	}
	return p
}

// Call returns the tool that runs the operation and its arguments: the
// filled path, the method for a request, and query, form or json holding
// every required parameter. A parameter takes values[name]; without one it
// takes its only known value, else the placeholder "<name>". A download
// gets save_to "<new file>".
func (o Operation) Call(values map[string]string) (toolName string, args map[string]any) {
	toolName = "developer_api_request"
	args = map[string]any{"path": o.FilledPath(values)}
	if o.Read {
		toolName = "developer_api_get"
	} else {
		args["method"] = o.Method
	}
	if o.Wire == WireJSON {
		// An empty body still goes as JSON, with the team id.
		args["json"] = map[string]any{}
	}
	for _, p := range o.Params {
		if !p.Required || p.In == "path" {
			continue
		}
		v := values[p.Name]
		switch {
		case v != "":
		case len(p.Values) == 1:
			v = p.Values[0].Value
		default:
			v = "<" + p.Name + ">"
		}
		body, _ := args[p.In].(map[string]any)
		if body == nil {
			body = map[string]any{}
			args[p.In] = body
		}
		if p.In != "json" {
			body[p.Name] = v
			continue
		}
		keys := strings.Split(p.Name, ".")
		for _, k := range keys[:len(keys)-1] {
			next, _ := body[k].(map[string]any)
			if next == nil {
				next = map[string]any{}
				body[k] = next
			}
			body = next
		}
		body[keys[len(keys)-1]] = v
	}
	if o.Wire == WireDownload {
		args["save_to"] = "<new file>"
	}
	return toolName, args
}
