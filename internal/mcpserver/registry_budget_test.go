package mcpserver_test

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
			logLargestTools(t, srv, 10)
		}
	}
}

// logLargestTools logs the top listed tools by marshalled size, largest first.
func logLargestTools(t *testing.T, srv *mcp.Server, top int) {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "hamn-budget", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	type sized struct {
		name  string
		bytes int
	}
	var all []sized
	for tl, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(tl)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, sized{tl.Name, len(b)})
	}
	slices.SortFunc(all, func(a, b sized) int { return cmp.Compare(b.bytes, a.bytes) })
	for _, s := range all[:min(top, len(all))] {
		t.Logf("  %6d bytes  %s", s.bytes, s.name)
	}
}

// readSuffixes, writeVerbs and destructiveVerbs map a tool name's final
// segment to its class (§4.2); classExceptions lists the names that do not
// follow the rule, with the class they must have.
var (
	readSuffixes = []string{
		"_list", "_get", "_status", "_plan", "_pull", "_report", "_search",
		"_describe", "_lookup", "_summary", "_wait",
	}
	writeVerbs = []string{
		"create", "add", "set", "update", "upsert", "upload", "attach", "prepare", "distribute",
		"start", "enable", "setup", "generate", "invite", "register", "sync",
	}
	destructiveVerbs = []string{
		"delete", "remove", "expire", "submit", "cancel", "release", "publish", "reply", "revoke", "phased",
	}
	classExceptions = map[string]tool.Class{
		"doctor":          tool.Read,
		"asc_api_request": tool.Destructive, // generic mutation, any method
		"metadata_apply":  tool.Write,       // applies a metadata_plan; deletes only listed locales
		"pricing_apply":   tool.Destructive, // apply-pricing (§4.2)
		"users_invite":    tool.Destructive, // Apple emails the invitee immediately
		// W verbs declared D in their packages; reported to Main, kept as shipped.
		"signing_capabilities_set": tool.Destructive, // can disable capabilities
		"webhooks_upsert":          tool.Destructive, // sends App Store events to an external URL
	}
)

// expectedClass returns the class the name's verb implies, or false when the
// verb is unknown.
func expectedClass(name string) (tool.Class, bool) {
	if c, ok := classExceptions[name]; ok {
		return c, true
	}
	for _, s := range readSuffixes {
		if strings.HasSuffix(name, s) {
			return tool.Read, true
		}
	}
	verb := name[strings.LastIndexByte(name, '_')+1:]
	switch {
	case slices.Contains(writeVerbs, verb):
		return tool.Write, true
	case slices.Contains(destructiveVerbs, verb):
		return tool.Destructive, true
	}
	return 0, false
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
		if m.Class == tool.Read && !m.Idempotent {
			t.Errorf("read tool %s is not Idempotent", m.Name)
		}
		if strings.HasPrefix(m.Name, "toolsets") {
			continue // server-local session control, classed in mcpserver
		}
		want, ok := expectedClass(m.Name)
		switch {
		case !ok:
			t.Errorf("tool %s: unknown verb; use a suffix from %v, %v or %v, or add an exception", m.Name, readSuffixes, writeVerbs, destructiveVerbs)
		case m.Class != want:
			t.Errorf("tool %s: class %v, its verb implies %v", m.Name, m.Class, want)
		}
	}
}
