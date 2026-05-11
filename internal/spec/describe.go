package spec

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/idoo/hamn/internal/tool"
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
	example := map[string]any{"method": op.Method, "path": op.Path}
	if b := op.Body; b != nil {
		d.BodyType = b.Type
		d.BodySkeleton = skeleton(b)
		example["body"] = d.BodySkeleton
		d.Params = append(d.Params, bodyParams(b)...)
	}
	d.Example = example
	return d
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
	for _, name := range sortedKeys(b.Attrs) {
		a := b.Attrs[name]
		out = append(out, tool.Param{Name: "data.attributes." + name, In: "body", Required: a.Required, Type: a.Type, Enum: a.Enum})
	}
	for _, name := range sortedKeys(b.Rels) {
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

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinLimited joins up to n values, noting how many were omitted.
func joinLimited(vals []string, n int) string {
	if len(vals) <= n {
		return strings.Join(vals, ", ")
	}
	return fmt.Sprintf("%s, … (%d more)", strings.Join(vals[:n], ", "), len(vals)-n)
}
