package tool_test

import (
	"context"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type scalarsIn struct {
	N     int      `json:"n,omitempty"`
	F     float64  `json:"f,omitempty"`
	B     bool     `json:"b,omitempty"`
	S     string   `json:"s,omitempty"`
	Ns    []int    `json:"ns,omitempty"`
	Bs    []bool   `json:"bs,omitempty"`
	Names []string `json:"names,omitempty"`
}

func TestCoerceScalarStrings(t *testing.T) {
	var got scalarsIn
	tl := tool.New(tool.Meta{
		Name: "scalars_list", Toolset: tool.Core, Class: tool.Read, Description: "scalars",
	}, func(_ context.Context, _ *tool.Env, in scalarsIn) (*tool.Result, error) {
		got = in
		return tool.OK(map[string]any{}), nil
	})
	k := tooltest.New(t)
	ok := []struct {
		args string
		want func(scalarsIn) bool
	}{
		{`{"n":"3"}`, func(in scalarsIn) bool { return in.N == 3 }},
		{`{"n":"-12"}`, func(in scalarsIn) bool { return in.N == -12 }},
		{`{"f":"2.5"}`, func(in scalarsIn) bool { return in.F == 2.5 }},
		{`{"f":"1e2"}`, func(in scalarsIn) bool { return in.F == 100 }},
		{`{"b":"true"}`, func(in scalarsIn) bool { return in.B }},
		{`{"ns":["1","5"]}`, func(in scalarsIn) bool { return len(in.Ns) == 2 && in.Ns[1] == 5 }},
		{`{"bs":["false"]}`, func(in scalarsIn) bool { return len(in.Bs) == 1 && !in.Bs[0] }},
		{`{"ns":"[1,2]"}`, func(in scalarsIn) bool { return len(in.Ns) == 2 }},
		// String-typed properties keep numeric/boolean spellings verbatim.
		{`{"s":"3","names":["true","7"]}`, func(in scalarsIn) bool {
			return in.S == "3" && len(in.Names) == 2 && in.Names[0] == "true" && in.Names[1] == "7"
		}},
	}
	for _, tt := range ok {
		got = scalarsIn{}
		tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, tt.args))
		if !tt.want(got) {
			t.Errorf("%s: decoded %+v", tt.args, got)
		}
	}
	for _, args := range []string{
		`{"n":"3.5"}`, `{"n":"03"}`, `{"n":"+3"}`, `{"n":" 3"}`, `{"n":"1e2"}`, `{"n":"9007199254740993"}`,
		`{"f":"NaN"}`, `{"f":"Infinity"}`, `{"f":"2."}`, `{"f":".5"}`, `{"f":"0x10"}`,
		`{"b":"yes"}`, `{"b":"True"}`, `{"b":"1"}`, `{"ns":["1","x"]}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), "invalid_arguments")
	}
}

func TestRangesReplaceIntegerEnums(t *testing.T) {
	tl := tool.New(tool.Meta{
		Name: "ranges_list", Toolset: tool.Core, Class: tool.Read, Description: "ranges",
		Ranges: map[string][2]int{"ns": {1, 5}},
	}, func(context.Context, *tool.Env, scalarsIn) (*tool.Result, error) {
		return tool.OK(map[string]any{}), nil
	})
	items := tl.InputSchema().Properties["ns"].Items
	if items.Minimum == nil || *items.Minimum != 1 || items.Maximum == nil || *items.Maximum != 5 || items.Enum != nil {
		t.Fatalf("items = %+v", items)
	}
	k := tooltest.New(t)
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"ns":["1",5]}`))
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"ns":[6]}`), "invalid_arguments")
	defer func() {
		if recover() == nil {
			t.Error("integer enum accepted")
		}
	}()
	tool.New(tool.Meta{
		Name: "enums_list", Toolset: tool.Core, Class: tool.Read, Description: "enums",
		Enums: map[string][]any{"n": {1, 2}},
	}, func(context.Context, *tool.Env, scalarsIn) (*tool.Result, error) {
		return tool.OK(map[string]any{}), nil
	})
}
