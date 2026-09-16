package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// maxEnumInProblem bounds how many allowed values a problem message lists.
const maxEnumInProblem = 12

// Validate checks a request against the spec: path template, method, query
// keys and enum values, and the JSON:API body (data.type, id, required and
// known attributes and relationships, enum values). Failures are
// *tool.ValidationError carrying the describe skeleton. It implements
// [tool.Spec].
func (x *Index) Validate(req tool.Request) error {
	method := strings.ToUpper(req.Method)
	path, rawQuery, _ := strings.Cut(req.Path, "?")
	op, ok := x.match(method, path)
	if !ok {
		return &tool.ValidationError{Problems: []string{x.noMatch(method, path)}}
	}
	query := req.Query
	if query == nil && rawQuery != "" {
		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			return &tool.ValidationError{Problems: []string{"malformed query string: " + err.Error()}, Skeleton: x.describe(op)}
		}
		query = parsed
	}
	v := validator{x: x, op: op}
	v.query(query)
	v.body(req, path)
	if len(v.problems) == 0 {
		return nil
	}
	return &tool.ValidationError{Problems: v.problems, Skeleton: x.describe(op)}
}

func (x *Index) noMatch(method, path string) string {
	var allowed []string
	for _, t := range x.candidates(path) {
		if !slices.Contains(allowed, t.op.Method) {
			allowed = append(allowed, t.op.Method)
		}
	}
	if len(allowed) > 0 {
		slices.Sort(allowed)
		return fmt.Sprintf("method %s is not defined for %s; allowed: %s", method, path, strings.Join(allowed, ", "))
	}
	return fmt.Sprintf("no operation matches %s %s (use asc_api_search)", method, path)
}

type validator struct {
	x        *Index
	op       *Op
	problems []string
}

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) query(q url.Values) {
	params := map[string]Param{}
	for _, p := range v.op.Params {
		if p.In == "query" {
			params[p.Name] = p
		}
	}
	for _, key := range slices.Sorted(maps.Keys(q)) {
		p, ok := params[key]
		if !ok {
			if key == "cursor" && v.op.Method == http.MethodGet {
				continue
			}
			v.addf("unknown query parameter %q for %s", key, v.op.ID)
			continue
		}
		for _, raw := range q[key] {
			vals := []string{raw}
			if p.Array {
				vals = strings.Split(raw, ",")
			}
			for _, val := range vals {
				v.queryValue(p, val)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(params)) {
		if params[name].Required && len(q[name]) == 0 {
			v.addf("missing required query parameter %q", name)
		}
	}
}

func (v *validator) queryValue(p Param, val string) {
	enum := p.Enum
	if t, ok := fieldsType(p.Name); ok {
		enum = v.x.file.Fields[t]
	}
	switch {
	case len(enum) > 0:
		if !slices.Contains(enum, val) {
			v.addf("%s: %q is not one of %s", p.Name, val, joinLimited(enum, maxEnumInProblem))
		}
	case p.Type == "integer":
		if _, err := strconv.Atoi(val); err != nil {
			v.addf("%s: %q is not an integer", p.Name, val)
		}
	case p.Type == "boolean":
		if val != "true" && val != "false" {
			v.addf("%s: %q is not a boolean", p.Name, val)
		}
	}
}

func (v *validator) body(req tool.Request, path string) {
	doc, present, err := decodeBody(req)
	if err != nil {
		v.addf("body is not valid JSON: %v", err)
		return
	}
	b := v.op.Body
	switch {
	case b == nil && present:
		v.addf("%s takes no request body", v.op.ID)
	case b != nil && !present && v.op.BodyNeeded:
		v.addf("%s requires a request body with data.type %q", v.op.ID, b.Type)
	case b != nil && present:
		v.document(b, doc, path)
	}
}

func decodeBody(req tool.Request) (doc any, present bool, err error) {
	var raw []byte
	switch {
	case req.RawBody != nil:
		if req.ContentType != "" && !strings.Contains(req.ContentType, "json") {
			return nil, false, nil
		}
		raw = req.RawBody
	case req.Body != nil:
		if raw, err = json.Marshal(req.Body); err != nil {
			return nil, true, fmt.Errorf("encode body: %w", err)
		}
	default:
		return nil, false, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, true, fmt.Errorf("decode body: %w", err)
	}
	return doc, doc != nil, nil
}

func (v *validator) document(b *Body, doc any, path string) {
	root, ok := doc.(map[string]any)
	if !ok {
		v.addf("body must be a JSON object with a data member")
		return
	}
	data, ok := root["data"]
	if !ok {
		v.addf("body is missing data")
		return
	}
	if b.Linkage {
		v.linkage("data", b.Type, b.Many, data)
		return
	}
	obj, ok := data.(map[string]any)
	if !ok {
		v.addf("data must be an object")
		return
	}
	if t, _ := obj["type"].(string); t != b.Type {
		v.addf("data.type must be %q, got %q", b.Type, t)
	}
	if b.ID {
		v.id(obj, path)
	}
	v.attributes(b, obj["attributes"])
	v.relationships(b, obj["relationships"])
}

// id checks data.id is present and equals the {id} path segment.
func (v *validator) id(obj map[string]any, path string) {
	id, _ := obj["id"].(string)
	if id == "" {
		v.addf("data.id is required")
		return
	}
	tmpl := splitPath(v.op.Path)
	segs := splitPath(path)
	if i := slices.Index(tmpl, "{id}"); i >= 0 && i < len(segs) && segs[i] != id {
		v.addf("data.id %q does not match the path id %q", id, segs[i])
	}
}

func (v *validator) attributes(b *Body, raw any) {
	attrs, _ := raw.(map[string]any)
	if raw != nil && attrs == nil {
		v.addf("data.attributes must be an object")
	}
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a, ok := b.Attrs[name]
		if !ok {
			v.addf("unknown attribute data.attributes.%s for %s", name, b.Type)
			continue
		}
		if len(a.Enum) > 0 {
			v.attrEnum(name, a.Enum, attrs[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(b.Attrs)) {
		if _, ok := attrs[name]; b.Attrs[name].Required && !ok {
			v.addf("missing required attribute data.attributes.%s", name)
		}
	}
}

func (v *validator) attrEnum(name string, enum []string, val any) {
	vals := []any{val}
	if list, ok := val.([]any); ok {
		vals = list
	}
	for _, e := range vals {
		s, ok := e.(string)
		if e == nil || (ok && slices.Contains(enum, s)) {
			continue
		}
		v.addf("data.attributes.%s: %v is not one of %s", name, e, joinLimited(enum, maxEnumInProblem))
	}
}

func (v *validator) relationships(b *Body, raw any) {
	rels, _ := raw.(map[string]any)
	if raw != nil && rels == nil {
		v.addf("data.relationships must be an object")
	}
	for _, name := range slices.Sorted(maps.Keys(rels)) {
		r, ok := b.Rels[name]
		if !ok {
			v.addf("unknown relationship data.relationships.%s for %s", name, b.Type)
			continue
		}
		obj, _ := rels[name].(map[string]any)
		data, ok := obj["data"]
		if !ok {
			v.addf("data.relationships.%s must have data", name)
			continue
		}
		v.linkage("data.relationships."+name+".data", r.Type, r.Many, data)
	}
	for _, name := range slices.Sorted(maps.Keys(b.Rels)) {
		if _, ok := rels[name]; b.Rels[name].Required && !ok {
			v.addf("missing required relationship data.relationships.%s (type %s)", name, b.Rels[name].Type)
		}
	}
}

// linkage checks resource identifiers {type,id} (or an array of them).
func (v *validator) linkage(at, typ string, many bool, data any) {
	if !many && data == nil {
		return // null clears a to-one relationship
	}
	items := []any{data}
	if many {
		list, ok := data.([]any)
		if !ok {
			v.addf("%s must be an array of {type, id}", at)
			return
		}
		items = list
	}
	for _, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			v.addf("%s must be {type, id}", at)
			continue
		}
		if t, _ := obj["type"].(string); typ != "" && t != typ {
			v.addf("%s type must be %q, got %q", at, typ, t)
		}
		if id, _ := obj["id"].(string); id == "" {
			v.addf("%s id is required", at)
		}
	}
}
