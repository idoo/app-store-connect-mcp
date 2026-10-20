package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // real registry
)

// listAll connects a client to a server with every toolset and writes on and
// returns its tools/list plus the server info it reported.
func listAll(t *testing.T) ([]*mcp.Tool, *mcp.InitializeResult) {
	t.Helper()
	ctx := context.Background()
	srv, err := mcpserver.Build(ctx, mcpserver.Deps{
		Profile:  tool.ProfileInfo{Name: "meta", KeyKind: tool.KeyTeam, AllowWrites: true},
		Toolsets: tool.AllToolsets,
		Version:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "meta", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	var out []*mcp.Tool
	for tl, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tl)
	}
	return out, cs.InitializeResult()
}

// TestToolsListOrderDeterministic checks two independent servers list the
// same tools in the same, name-sorted order.
func TestToolsListOrderDeterministic(t *testing.T) {
	names := func() []string {
		tools, _ := listAll(t)
		out := make([]string, len(tools))
		for i, tl := range tools {
			out[i] = tl.Name
		}
		return out
	}
	a, b := names(), names()
	if !slices.Equal(a, b) {
		t.Fatalf("order differs:\n%v\n%v", a, b)
	}
	if !slices.IsSorted(a) {
		t.Fatalf("tools/list is not name-sorted: %v", a)
	}
}

// TestServerInfo checks the advertised implementation: description equal to
// server.json and only inline data: icons.
func TestServerInfo(t *testing.T) {
	_, init := listAll(t)
	info := init.ServerInfo
	raw, err := os.ReadFile("../../server.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Description string `json:"description"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if info.Description != manifest.Description || info.Title != manifest.Title {
		t.Errorf("serverInfo title/description = %q/%q, server.json %q/%q", info.Title, info.Description, manifest.Title, manifest.Description)
	}
	if info.WebsiteURL == "" {
		t.Error("serverInfo has no websiteUrl")
	}
	if len(info.Icons) == 0 {
		t.Fatal("serverInfo has no icons")
	}
	for _, ic := range info.Icons {
		if !strings.HasPrefix(ic.Source, "data:image/svg+xml;base64,") {
			t.Errorf("icon %q is not an inline SVG data: URI", ic.Source)
		}
	}
}
