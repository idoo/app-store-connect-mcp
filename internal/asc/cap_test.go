package asc

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func sizeOf(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func makeRes(typ string, n int) []tool.Resource {
	rs := make([]tool.Resource, n)
	for i := range rs {
		rs[i] = tool.Resource{ID: fmt.Sprint(i), Type: typ, Attributes: map[string]any{"name": strings.Repeat("x", 40)}}
	}
	return rs
}

func TestCapResultListResult(t *testing.T) {
	full := &tool.ListResult{Data: makeRes("apps", 10), Included: makeRes("builds", 3), Total: 10, Returned: 10, NextCursor: "c"}
	noIncl := &tool.ListResult{Data: makeRes("apps", 10), Total: 10, Returned: 10, NextCursor: "c"}
	four := &tool.ListResult{Data: makeRes("apps", 4), Total: 10, Returned: 4, NextCursor: "c"}

	tests := []struct {
		name         string
		max          int
		wantReturned int
		wantDropped  int
		wantIncluded int
		wantTrunc    bool
	}{
		{name: "fits", max: sizeOf(t, full), wantReturned: 10, wantIncluded: 3},
		{name: "disabled", max: 0, wantReturned: 10, wantIncluded: 3},
		{name: "included dropped first", max: sizeOf(t, noIncl), wantReturned: 10, wantDropped: 3, wantTrunc: true},
		{name: "then tail items", max: sizeOf(t, four), wantReturned: 4, wantDropped: 3 + 6, wantTrunc: true},
		{name: "nothing fits", max: 5, wantReturned: 0, wantDropped: 3 + 10, wantTrunc: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, tr, err := CapResult(full, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			lr := got.(*tool.ListResult)
			if lr.Returned != tt.wantReturned || len(lr.Data) != tt.wantReturned || len(lr.Included) != tt.wantIncluded {
				t.Errorf("returned=%d data=%d included=%d", lr.Returned, len(lr.Data), len(lr.Included))
			}
			if wantCursor := map[bool]string{true: "c"}[tt.wantReturned == len(full.Data)]; lr.NextCursor != wantCursor {
				t.Errorf("next_cursor = %q, want %q (a cursor after dropped items would skip them)", lr.NextCursor, wantCursor)
			}
			if (tr != nil) != tt.wantTrunc {
				t.Fatalf("truncation = %+v, want present=%v", tr, tt.wantTrunc)
			}
			if tr != nil {
				if tr.Dropped != tt.wantDropped || !strings.Contains(tr.Hint, fmt.Sprintf("%d bytes", tt.max)) {
					t.Errorf("truncation = %+v, want dropped %d", tr, tt.wantDropped)
				}
				if tt.max > 5 && sizeOf(t, got) > tt.max {
					t.Errorf("result %d bytes exceeds cap %d", sizeOf(t, got), tt.max)
				}
			}
		})
	}
	if len(full.Data) != 10 || len(full.Included) != 3 {
		t.Fatal("CapResult mutated its input list")
	}
}

func TestCapResultGeneric(t *testing.T) {
	builds := make([]any, 8)
	for i := range builds {
		builds[i] = map[string]any{"id": i, "v": strings.Repeat("b", 30)}
	}
	obj := func() map[string]any {
		return map[string]any{
			"app":      map[string]any{"name": "Hjul"},
			"tags":     []any{"a", "b"},
			"builds":   builds,
			"included": []any{map[string]any{"id": "x"}, map[string]any{"id": "y"}},
		}
	}
	want3 := map[string]any{"app": map[string]any{"name": "Hjul"}, "tags": []any{"a", "b"}, "builds": builds[:3]}
	noIncl := obj()
	delete(noIncl, "included")

	tests := []struct {
		name        string
		in          any
		max         int
		wantDropped int
		check       func(t *testing.T, got any)
	}{
		{
			name: "object drops included then largest array tail", in: obj(), max: sizeOf(t, want3), wantDropped: 2 + 5,
			check: func(t *testing.T, got any) {
				m := got.(map[string]any)
				if _, ok := m["included"]; ok || len(m["builds"].([]any)) != 3 || len(m["tags"].([]any)) != 2 {
					t.Errorf("got %v", m)
				}
			},
		},
		{
			name: "object with cursor drops it and fixes returned", in: map[string]any{"builds": builds, "returned": 8, "next_cursor": "c"},
			max: sizeOf(t, map[string]any{"builds": builds[:3], "returned": 8, "next_cursor": "c"}), wantDropped: 5,
			check: func(t *testing.T, got any) {
				m := got.(map[string]any)
				if _, ok := m["next_cursor"]; ok || m["returned"] != 3 || len(m["builds"].([]any)) != 3 {
					t.Errorf("got %v, want 3 builds, returned 3, no next_cursor", m)
				}
			},
		},
		{
			name: "object only included", in: obj(), max: sizeOf(t, noIncl), wantDropped: 2,
			check: func(t *testing.T, got any) {
				if len(got.(map[string]any)["builds"].([]any)) != 8 {
					t.Errorf("builds trimmed unnecessarily")
				}
			},
		},
		{
			name: "top-level array tail", in: builds, max: sizeOf(t, builds[:2]), wantDropped: 6,
			check: func(t *testing.T, got any) {
				if len(got.([]any)) != 2 {
					t.Errorf("got %d items", len(got.([]any)))
				}
			},
		},
		{
			name: "struct converted generically", in: struct {
				Items []string `json:"items"`
			}{Items: []string{"aaaa", "bbbb", "cccc"}}, max: len(`{"items":["aaaa"]}`), wantDropped: 2,
			check: func(t *testing.T, got any) {
				if sizeOf(t, got) != len(`{"items":["aaaa"]}`) {
					t.Errorf("got %v", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, tr, err := CapResult(tt.in, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			if tr == nil || tr.Dropped != tt.wantDropped {
				t.Fatalf("truncation = %+v, want dropped %d", tr, tt.wantDropped)
			}
			if sizeOf(t, got) > tt.max {
				t.Errorf("result exceeds cap")
			}
			tt.check(t, got)
		})
	}
}

func TestCapResultMarshalError(t *testing.T) {
	if _, _, err := CapResult(map[string]any{"f": func() {}}, 10); err == nil {
		t.Fatal("want marshal error")
	}
}
