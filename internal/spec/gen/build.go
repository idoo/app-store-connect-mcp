package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/idoo/app-store-connect-mcp/internal/spec"
)

type node = map[string]any

var methods = []string{"get", "post", "patch", "put", "delete"}

// freeTextHints are attribute-name fragments (lowercase) marking text written
// by people or third parties.
var freeTextHints = []string{
	"description", "body", "text", "note", "comment", "title", "whatsnew",
	"keyword", "reason", "message", "feedback", "name",
}

// Build converts Apple's OpenAPI JSON into the compact index.
func Build(raw []byte) (*spec.File, error) {
	var root node
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode spec: %w", err)
	}
	r := resolver{root: root}
	info, _ := root["info"].(node)
	version, _ := info["version"].(string)
	paths, _ := root["paths"].(node)
	if version == "" || len(paths) == 0 {
		return nil, errors.New("spec has no info.version or paths")
	}
	f := &spec.File{Version: version, Paths: len(paths), Fields: map[string][]string{}}
	fields := map[string]map[string]bool{}
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		item := r.deref(paths[p])
		shared := r.params(item["parameters"])
		for _, m := range methods {
			raw, ok := item[m]
			if !ok {
				continue
			}
			op, err := r.op(strings.ToUpper(m), p, r.deref(raw), shared, fields)
			if err != nil {
				return nil, err
			}
			f.Ops = append(f.Ops, op)
		}
	}
	for t, set := range fields {
		f.Fields[t] = slices.Sorted(maps.Keys(set))
	}
	f.FreeText = r.freeText()
	return f, nil
}

type resolver struct{ root node }

// deref follows local JSON pointers ("#/components/schemas/X").
func (r resolver) deref(v any) node {
	n, _ := v.(node)
	for range 32 {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}
		n = r.pointer(ref)
	}
	return n
}

func (r resolver) pointer(ref string) node {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	cur := r.root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		next, ok := cur[part].(node)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func (r resolver) params(v any) []spec.Param {
	list, _ := v.([]any)
	out := make([]spec.Param, 0, len(list))
	for _, raw := range list {
		p := r.deref(raw)
		name, _ := p["name"].(string)
		in, _ := p["in"].(string)
		req, _ := p["required"].(bool)
		t, _, enum, array := r.schemaInfo(p["schema"])
		out = append(out, spec.Param{Name: name, In: in, Type: t, Array: array, Required: req, Enum: enum})
	}
	return out
}

// schemaInfo returns type, format, enum and whether the schema is an array
// (in which case type/enum describe the items).
func (r resolver) schemaInfo(v any) (typ, format string, enum []string, array bool) {
	s := r.deref(v)
	if t, _ := s["type"].(string); t == "array" {
		array = true
		s = r.deref(s["items"])
	}
	typ, _ = s["type"].(string)
	format, _ = s["format"].(string)
	if list, ok := s["enum"].([]any); ok {
		for _, e := range list {
			if str, ok := e.(string); ok {
				enum = append(enum, str)
			}
		}
	}
	return typ, format, enum, array
}

func (r resolver) op(method, path string, o node, shared []spec.Param, fields map[string]map[string]bool) (spec.Op, error) {
	id, _ := o["operationId"].(string)
	if id == "" {
		return spec.Op{}, fmt.Errorf("%s %s has no operationId", method, path)
	}
	op := spec.Op{ID: id, Method: method, Path: path, Summary: summarize(id)}
	op.Deprecated, _ = o["deprecated"].(bool)
	if tags, ok := o["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				op.Tags = append(op.Tags, s)
			}
		}
	}
	for _, p := range append(append([]spec.Param{}, shared...), r.params(o["parameters"])...) {
		if t, ok := strings.CutPrefix(p.Name, "fields["); ok && len(p.Enum) > 0 {
			t = strings.TrimSuffix(t, "]")
			if fields[t] == nil {
				fields[t] = map[string]bool{}
			}
			for _, e := range p.Enum {
				fields[t][e] = true
			}
			p.Enum = nil
		}
		op.Params = append(op.Params, p)
	}
	if rb := r.deref(o["requestBody"]); rb != nil {
		op.BodyNeeded, _ = rb["required"].(bool)
		op.Body = r.body(jsonSchema(rb), path)
	}
	r.response(&op, o)
	return op, nil
}

func jsonSchema(n node) any {
	content, _ := n["content"].(node)
	mt, _ := content["application/json"].(node)
	return mt["schema"]
}

func (r resolver) body(schemaRef any, path string) *spec.Body {
	s := r.deref(schemaRef)
	props, _ := s["properties"].(node)
	data := r.deref(props["data"])
	if data == nil {
		return nil
	}
	if t, _ := data["type"].(string); t == "array" {
		typ := r.resourceType(r.deref(data["items"]))
		return &spec.Body{Type: typ, Linkage: true, Many: true}
	}
	b := &spec.Body{Type: r.resourceType(data)}
	dp, _ := data["properties"].(node)
	req := stringSet(data["required"])
	b.ID = req["id"]
	if _, ok := dp["attributes"]; !ok {
		if _, ok := dp["relationships"]; !ok && strings.Contains(path, "/relationships/") {
			b.Linkage = true
			return b
		}
	}
	if attrs := r.deref(dp["attributes"]); attrs != nil {
		ap, _ := attrs["properties"].(node)
		areq := stringSet(attrs["required"])
		b.Attrs = map[string]spec.Attr{}
		for name, v := range ap {
			t, f, e, arr := r.schemaInfo(v)
			if arr {
				t = "array"
			}
			b.Attrs[name] = spec.Attr{Type: t, Format: f, Required: areq[name], Enum: e}
		}
	}
	if rels := r.deref(dp["relationships"]); rels != nil {
		rp, _ := rels["properties"].(node)
		rreq := stringSet(rels["required"])
		b.Rels = map[string]spec.Rel{}
		for name, v := range rp {
			rprops, _ := r.deref(v)["properties"].(node)
			rd := r.deref(rprops["data"])
			rel := spec.Rel{Required: rreq[name]}
			if t, _ := rd["type"].(string); t == "array" {
				rel.Many = true
				rd = r.deref(rd["items"])
			}
			rel.Type = r.resourceType(rd)
			b.Rels[name] = rel
		}
	}
	return b
}

// resourceType reads the single enum value of a JSON:API object's "type".
func (r resolver) resourceType(obj node) string {
	props, _ := obj["properties"].(node)
	_, _, enum, _ := r.schemaInfo(props["type"])
	if len(enum) == 1 {
		return enum[0]
	}
	return ""
}

func (r resolver) response(op *spec.Op, o node) {
	responses, _ := o["responses"].(node)
	for _, code := range []string{"200", "201"} {
		resp := r.deref(responses[code])
		if resp == nil {
			continue
		}
		content, _ := resp["content"].(node)
		for ct := range content {
			if ct != "application/json" {
				op.ContentType = ct
			}
		}
		s := r.deref(jsonSchema(resp))
		props, _ := s["properties"].(node)
		data := r.deref(props["data"])
		if t, _ := data["type"].(string); t == "array" {
			op.RespMany = true
			data = r.deref(data["items"])
		}
		op.RespType = r.resourceType(data)
		return
	}
}

func (r resolver) freeText() map[string][]string {
	comps, _ := r.root["components"].(node)
	schemas, _ := comps["schemas"].(node)
	out := map[string]map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(schemas)) {
		s := r.deref(schemas[name])
		props, _ := s["properties"].(node)
		typ := r.resourceType(s)
		attrs := r.deref(props["attributes"])
		if typ == "" || attrs == nil || strings.HasSuffix(name, "Request") {
			continue
		}
		ap, _ := attrs["properties"].(node)
		for attr, v := range ap {
			t, f, enum, _ := r.schemaInfo(v)
			if t != "string" || len(enum) > 0 || f != "" || !isFreeText(attr) {
				continue
			}
			if out[typ] == nil {
				out[typ] = map[string]bool{}
			}
			out[typ][attr] = true
		}
	}
	res := make(map[string][]string, len(out))
	for t, set := range out {
		res[t] = slices.Sorted(maps.Keys(set))
	}
	return res
}

func isFreeText(attr string) bool {
	l := strings.ToLower(attr)
	if strings.HasSuffix(l, "url") || strings.HasSuffix(l, "id") {
		return false
	}
	for _, h := range freeTextHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

func stringSet(v any) map[string]bool {
	list, _ := v.([]any)
	set := make(map[string]bool, len(list))
	for _, s := range list {
		if str, ok := s.(string); ok {
			set[str] = true
		}
	}
	return set
}

// summarize derives a short English summary from Apple's operationId
// convention (resource_[relationship_]action); the 4.x spec has none.
func summarize(id string) string {
	parts := strings.Split(id, "_")
	action := parts[len(parts)-1]
	res := words(parts[0])
	rel := ""
	if len(parts) > 2 {
		rel = words(parts[1])
	}
	switch action {
	case "getCollection":
		return "List " + res
	case "getInstance":
		return "Get one of " + res
	case "createInstance":
		return "Create " + res
	case "updateInstance":
		return "Update " + res
	case "deleteInstance":
		return "Delete " + res
	case "getToManyRelated", "getToOneRelated":
		return "Get " + rel + " of " + res
	case "getToManyRelationship", "getToOneRelationship":
		return "Get " + rel + " ids of " + res
	case "createToManyRelationship":
		return "Add " + rel + " to " + res
	case "deleteToManyRelationship":
		return "Remove " + rel + " from " + res
	case "replaceToManyRelationship", "updateToOneRelationship":
		return "Set " + rel + " of " + res
	case "getMetrics":
		return "Get " + rel + " metrics of " + res
	}
	return strings.Join(parts, " ")
}

// words splits camelCase into lowercase words.
func words(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && unicode.IsUpper(c) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}
