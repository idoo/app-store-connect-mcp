package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// singleInlineMax is the largest single resource asc_api_get returns inline.
// It stays below the default 60 KB result cap: a bigger resource would be
// dropped whole by the cap (data:[] with total:1), so it is stored as an
// artifact and summarised instead (e.g. /v1/appAssetLibraryRefData).
const singleInlineMax = 48 << 10

// oversizeSingle reports whether rs is one resource too big to inline. When
// it is, out gets a summary of the resource and a link to its full JSON
// (stored with env.Artifacts) in place of data and included.
func oversizeSingle(ctx context.Context, env *tool.Env, out map[string]any, rs []tool.Resource) (*tool.Result, bool, error) {
	if len(rs) != 1 {
		return nil, false, nil
	}
	b, err := json.Marshal(rs[0])
	if err != nil {
		return nil, false, fmt.Errorf("encode resource: %w", err)
	}
	if len(b) <= singleInlineMax {
		return nil, false, nil
	}
	delete(out, "data")
	delete(out, "included")
	out["data_summary"] = summarizeResource(rs[0], len(b))
	res := tool.OK(out)
	hint := fmt.Sprintf("the resource is %d bytes, too large to inline", len(b))
	if env.Artifacts == nil {
		out["data_hint"] = hint + "; select attributes with fields[" + rs[0].Type + "]"
		return res, true, nil
	}
	link, err := env.Artifacts.Put(ctx, rs[0].Type+"-"+rs[0].ID+".json", "application/json", b)
	if err != nil {
		out["data_hint"] = hint + " and could not be stored: " + tool.BodyFor(err).Message + "; select attributes with fields[" + rs[0].Type + "]"
		return res, true, nil
	}
	out["data_resource"] = link.URI
	out["data_hint"] = hint + "; read data_resource for the full JSON, or select attributes with fields[" + rs[0].Type + "]"
	return res.WithLink(link), true, nil
}

// summarizeResource describes r without its values: each attribute and
// relationship key with an array's length, an object's key count, or the
// value's JSON kind.
func summarizeResource(r tool.Resource, size int) map[string]any {
	attrs := make(map[string]any, len(r.Attributes))
	for _, k := range slices.Sorted(maps.Keys(r.Attributes)) {
		attrs[k] = shape(r.Attributes[k])
	}
	s := map[string]any{"id": r.ID, "type": r.Type, "bytes": size, "attributes": attrs}
	if len(r.Rel) > 0 {
		rel := make(map[string]any, len(r.Rel))
		for k, v := range r.Rel {
			rel[k] = shape(v)
		}
		s["rel"] = rel
	}
	return s
}

// shape is the summary of one value: arrays by length, objects by key count.
func shape(v any) any {
	switch x := v.(type) {
	case []any:
		return map[string]int{"items": len(x)}
	case []string:
		return map[string]int{"items": len(x)}
	case map[string]any:
		return map[string]int{"keys": len(x)}
	case nil:
		return "null"
	case string, tool.Untrusted:
		return "string"
	case bool:
		return "bool"
	default:
		return "number"
	}
}
