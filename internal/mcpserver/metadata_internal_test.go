package mcpserver

import (
	"context"
	"regexp"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestUIProtocolVersion checks every embedded view sends the MCP Apps
// protocol version the server is written against, and only that one.
func TestUIProtocolVersion(t *testing.T) {
	re := regexp.MustCompile(`protocolVersion:"([^"]*)"`)
	for _, screen := range uiScreens {
		b, err := uiFS.ReadFile("ui/" + screen + ".html")
		if err != nil {
			t.Fatal(err)
		}
		m := re.FindAllSubmatch(b, -1)
		if len(m) == 0 {
			t.Errorf("%s: no ui/initialize protocolVersion", screen)
		}
		for _, v := range m {
			if string(v[1]) != uiProtocolVersion {
				t.Errorf("%s: protocolVersion %q, want %q", screen, v[1], uiProtocolVersion)
			}
		}
	}
}

// TestSetCacheable checks every cacheable result is private and only the
// static docs and lists get a TTL, with tools/list the shortest.
func TestSetCacheable(t *testing.T) {
	cases := []struct {
		name string
		req  mcp.Request
		ttl  int
	}{
		{"tools/list", &mcp.ListToolsRequest{}, toolsListTTLMs},
		{"resources/list", &mcp.ListResourcesRequest{}, listTTLMs},
		{"resources/templates/list", &mcp.ListResourceTemplatesRequest{}, listTTLMs},
		{"prompts/list", &mcp.ListPromptsRequest{}, listTTLMs},
		{"read static doc", &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: limitsURI}}, staticDocTTLMs},
		{"read apps", &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "app-store-connect-mcp://apps"}}, 0},
	}
	for _, c := range cases {
		var got mcp.Cacheable
		setCacheable(context.Background(), c.req, &got)
		if got.CacheScope != "private" || got.TTLMs != c.ttl {
			t.Errorf("%s: %+v, want private/%d", c.name, got, c.ttl)
		}
	}
	if toolsListTTLMs > listTTLMs {
		t.Error("tools/list TTL must be the shortest: toolsets change at runtime")
	}
}
