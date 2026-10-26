package spec

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Placeholders used in synthesized bodies (the spec carries no examples).
const (
	placeholderID     = "<id>"
	placeholderString = "<string>"
)

// Describe returns the compact description of an operation id. It
// implements [tool.Spec].
func (x *Index) Describe(operationID string) (*tool.OpDescription, error) {
	op, ok := x.byID[operationID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown operation id %q (use asc_api_search)", tool.ErrInvalidArgs, operationID)
	}
	return x.describe(op), nil
}

func (x *Index) describe(op *Op) *tool.OpDescription {
	d := &tool.OpDescription{Operation: toOperation(op), Sensitive: x.opSensitive(op)}
	for _, p := range op.Params {
		if t, ok := fieldsType(p.Name); ok {
			if d.Fields == nil {
				d.Fields = map[string][]string{}
			}
			d.Fields[t] = x.FieldNames(t)
			continue
		}
		if p.Name == "include" {
			d.Includes = append([]string(nil), p.Enum...)
			continue
		}
		d.Params = append(d.Params, tool.Param{Name: p.Name, In: p.In, Required: p.Required, Type: paramType(p), Enum: p.Enum})
	}
	example := map[string]any{"method": op.Method, "path": examplePath(op.Path)}
	if op.Method == http.MethodGet {
		// asc_api_get arguments: concrete fields/include/limit, not only the template.
		delete(example, "method")
		if f := preferredFields(d.Fields[targetType(op.ID)]); len(f) > 0 {
			example["fields"] = map[string]string{targetType(op.ID): strings.Join(f, ",")}
		}
		if len(d.Includes) > 0 {
			example["include"] = d.Includes[:1]
		}
		for _, p := range op.Params {
			if p.Name == "limit" {
				example["query"] = map[string]string{"limit": "50"}
			}
		}
	}
	if b := op.Body; b != nil {
		d.BodyType = b.Type
		d.BodySkeleton = skeleton(b)
		example["body"] = d.BodySkeleton
		d.Params = append(d.Params, bodyParams(b)...)
	}
	d.Example = example
	return d
}

// exampleFields caps the sparse fieldset shown in a GET example.
const exampleFields = 3

// preferredFields picks up to exampleFields names an agent most likely wants,
// in priority order: name, referenceName, title, then *State/*Status, then
// versionString, then *Date. It returns nil when none match, so the example
// never suggests an arbitrary (alphabetical) fieldset.
func preferredFields(names []string) []string {
	tiers := []func(string) bool{
		func(n string) bool { return n == "name" },
		func(n string) bool { return n == "referenceName" },
		func(n string) bool { return n == "title" },
		func(n string) bool {
			return strings.HasSuffix(n, "State") || strings.HasSuffix(n, "state") || strings.HasSuffix(n, "Status")
		},
		func(n string) bool { return n == "versionString" },
		func(n string) bool { return strings.HasSuffix(n, "Date") },
	}
	var out []string
	for _, match := range tiers {
		for _, n := range names {
			if len(out) == exampleFields {
				return out
			}
			if match(n) && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// examplePath fills each {param} of a template with a visible placeholder
// ("/v1/apps/{id}/builds" → "/v1/apps/<id>/builds").
func examplePath(template string) string {
	segs := strings.Split(template, "/")
	for i, s := range segs {
		if isParam(s) {
			segs[i] = "<" + strings.Trim(s, "{}") + ">"
		}
	}
	return strings.Join(segs, "/")
}

// targetType is the resource type an operation returns, from Apple's
// operation id naming (apps_appEvents_getToManyRelated → appEvents).
func targetType(opID string) string {
	parts := strings.Split(opID, "_")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return parts[0]
}

func (x *Index) opSensitive(op *Op) bool {
	if op.Method == http.MethodGet || op.Method == http.MethodHead {
		return false
	}
	c, ok := Classify(op.Method, op.Path)
	return !ok || c == tool.Destructive
}

func paramType(p Param) string {
	if p.Array {
		return "array"
	}
	return p.Type
}

// bodyParams lists body members as In:"body" params so the agent sees every
// optional attribute without a skeleton that would overwrite them.
func bodyParams(b *Body) []tool.Param {
	var out []tool.Param
	for _, name := range slices.Sorted(maps.Keys(b.Attrs)) {
		a := b.Attrs[name]
		out = append(out, tool.Param{Name: "data.attributes." + name, In: "body", Required: a.Required, Type: a.Type, Enum: a.Enum})
	}
	for _, name := range slices.Sorted(maps.Keys(b.Rels)) {
		r := b.Rels[name]
		t := "relationship"
		if r.Many {
			t = "relationship[]"
		}
		out = append(out, tool.Param{Name: "data.relationships." + name, In: "body", Required: r.Required, Type: t, Enum: []string{r.Type}})
	}
	return out
}

// skeleton synthesizes a minimal body: type, id when required, required
// attributes and required relationships.
func skeleton(b *Body) map[string]any {
	ident := func(t string) map[string]any { return map[string]any{"type": t, "id": placeholderID} }
	if b.Linkage {
		if b.Many {
			return map[string]any{"data": []any{ident(b.Type)}}
		}
		return map[string]any{"data": ident(b.Type)}
	}
	data := map[string]any{"type": b.Type}
	if b.ID {
		data["id"] = placeholderID
	}
	attrs := map[string]any{}
	for name, a := range b.Attrs {
		if a.Required {
			attrs[name] = placeholder(a)
		}
	}
	if len(attrs) > 0 || len(b.Attrs) > 0 {
		data["attributes"] = attrs
	}
	rels := map[string]any{}
	for name, r := range b.Rels {
		if !r.Required {
			continue
		}
		if r.Many {
			rels[name] = map[string]any{"data": []any{ident(r.Type)}}
		} else {
			rels[name] = map[string]any{"data": ident(r.Type)}
		}
	}
	if len(rels) > 0 {
		data["relationships"] = rels
	}
	return map[string]any{"data": data}
}

func placeholder(a Attr) any {
	if len(a.Enum) > 0 {
		return a.Enum[0]
	}
	switch a.Type {
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	if a.Format != "" {
		return "<" + a.Format + ">"
	}
	return placeholderString
}

// joinLimited joins up to n values, noting how many were omitted.
func joinLimited(vals []string, n int) string {
	if len(vals) <= n {
		return strings.Join(vals, ", ")
	}
	return fmt.Sprintf("%s, … (%d more)", strings.Join(vals[:n], ", "), len(vals)-n)
}
