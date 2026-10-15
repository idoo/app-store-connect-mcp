package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/mcpserver"
	"github.com/idoo/hamn/internal/tool"
	_ "github.com/idoo/hamn/internal/tools/all" // real registry
)

// TestRegistryBudget measures the real tools/list payload for the shipped
// registry and enforces the context-budget caps (default toolsets with writes
// on ≤ 40 KB).
func TestRegistryBudget(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		writes   bool
		toolsets []tool.Toolset
		limit    int
	}{
		{"default read-only", false, nil, 40 << 10},
		{"default writes", true, nil, 40 << 10},
		{"all writes", true, tool.AllToolsets, 110 << 10},
	}
	for _, c := range cases {
		srv, err := mcpserver.Build(ctx, mcpserver.Deps{
			Profile:  tool.ProfileInfo{Name: "budget", KeyKind: tool.KeyTeam, AllowWrites: c.writes},
			Toolsets: c.toolsets,
		})
		if err != nil {
			t.Fatal(err)
		}
		n, err := mcpserver.ListBytes(ctx, srv)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: tools/list = %d bytes (cap %d)", c.name, n, c.limit)
		if n > c.limit {
			t.Errorf("%s: tools/list = %d bytes, cap %d (over by %d)", c.name, n, c.limit, n-c.limit)
		}
	}
}

var readSuffixes = []string{
	"_list", "_get", "_status", "_plan", "_pull", "_report", "_search",
	"_describe", "_lookup", "_summary", "_wait",
}

func TestRegistryNamingAndMeta(t *testing.T) {
	for _, tl := range tool.All() {
		m := tl.Meta()
		if strings.TrimSpace(m.Title) == "" {
			t.Errorf("%s: missing Title", m.Name)
		}
		if len(m.Description) > tool.MaxDescription {
			t.Errorf("%s: description %d bytes > %d", m.Name, len(m.Description), tool.MaxDescription)
		}
		if m.Class != tool.Read || m.Name == "doctor" || strings.HasPrefix(m.Name, "toolsets") {
			continue
		}
		ok := false
		for _, s := range readSuffixes {
			ok = ok || strings.HasSuffix(m.Name, s)
		}
		if !ok {
			t.Errorf("read tool %s lacks an allowed suffix %v", m.Name, readSuffixes)
		}
	}
}
