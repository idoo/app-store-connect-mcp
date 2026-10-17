package mcpserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

var uiCaps = &mcp.ClientCapabilities{Extensions: map[string]any{
	uiExtension: map[string]any{"mimeTypes": []string{uiMIMEType}},
}}

// uiFixtureTools is the fixture set plus one Read tool per screen.
func uiFixtureTools(f *fixture) []tool.Tool {
	ts := f.tools()
	for _, screen := range uiScreens {
		ts = append(ts, stub("ui_"+screen, tool.Core, tool.Read, func(m *tool.Meta) { m.UI = screen }))
	}
	return ts
}

func listJSON(t *testing.T, h *harness) ([]*mcp.Tool, []byte) {
	t.Helper()
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.Tools)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools, b
}

func TestUIOnlyForUIClients(t *testing.T) {
	deps := func() Deps { return Deps{Tools: uiFixtureTools(&fixture{}), Profile: team(true)} }
	plain := connect(t, deps(), "test", nil)
	_, plainJSON := listJSON(t, plain)
	if strings.Contains(string(plainJSON), `"_meta"`) || strings.Contains(string(plainJSON), "ui://") {
		t.Fatalf("non-UI tools/list carries UI meta: %s", plainJSON)
	}
	// Declaring an unrelated extension, or the UI extension without the
	// mcp-app mime type, changes nothing.
	for _, caps := range []*mcp.ClientCapabilities{
		{Extensions: map[string]any{"other/ext": map[string]any{}}},
		{Extensions: map[string]any{uiExtension: map[string]any{"mimeTypes": []string{"text/html"}}}},
	} {
		h := connectOpts(t, deps(), "test", &mcp.ClientOptions{Capabilities: caps}, "2025-11-25")
		if _, b := listJSON(t, h); string(b) != string(plainJSON) {
			t.Fatalf("caps %v changed tools/list", caps.Extensions)
		}
	}
	for r, err := range plain.cs.Resources(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(r.URI, uiURIPrefix) {
			t.Fatalf("non-UI client sees %s", r.URI)
		}
	}

	ui := connectOpts(t, deps(), "test", &mcp.ClientOptions{Capabilities: uiCaps}, "2025-11-25")
	tools, _ := listJSON(t, ui)
	seen := 0
	var stripped []*mcp.Tool
	for _, tl := range tools {
		c := *tl
		if screen, ok := strings.CutPrefix(tl.Name, "ui_"); ok {
			seen++
			m, _ := tl.Meta["ui"].(map[string]any)
			if m["resourceUri"] != uiResourceURI(screen) {
				t.Fatalf("%s _meta = %v", tl.Name, tl.Meta)
			}
			c.Meta = nil
		} else if tl.Meta != nil {
			t.Fatalf("%s has _meta %v", tl.Name, tl.Meta)
		}
		stripped = append(stripped, &c)
	}
	if seen != len(uiScreens) {
		t.Fatalf("UI tools = %d, want %d", seen, len(uiScreens))
	}
	if b, _ := json.Marshal(stripped); string(b) != string(plainJSON) {
		t.Fatal("UI tools/list differs from non-UI beyond _meta.ui")
	}

	got := map[string]bool{}
	for r, err := range ui.cs.Resources(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(r.URI, uiURIPrefix) {
			got[r.URI] = true
			if r.MIMEType != uiMIMEType {
				t.Fatalf("%s mime = %q", r.URI, r.MIMEType)
			}
		}
	}
	for _, screen := range uiScreens {
		uri := uiResourceURI(screen)
		if !got[uri] {
			t.Fatalf("UI client misses %s", uri)
		}
		res, err := ui.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatal(err)
		}
		c := res.Contents[0]
		csp, _ := c.Meta["ui"].(map[string]any)["csp"].(map[string]any)
		if c.MIMEType != uiMIMEType || !strings.HasPrefix(c.Text, "<!doctype html>") || csp == nil {
			t.Fatalf("%s read = %q %v", uri, c.MIMEType, c.Meta)
		}
		for _, k := range []string{"connectDomains", "resourceDomains"} {
			if d, _ := csp[k].([]any); len(d) != 0 {
				t.Fatalf("%s csp %s = %v", uri, k, d)
			}
		}
	}
}

// A view's first click calls the tool without plan_id; the server's plan
// flow is unchanged for UI clients: previews never write.
func TestUIClientPlanFlowUnchanged(t *testing.T) {
	f := &fixture{}
	h := connectOpts(t, Deps{Tools: uiFixtureTools(f), Profile: team(true)}, "test", &mcp.ClientOptions{Capabilities: uiCaps}, "2025-11-25")
	res, body := h.call(t, "value_set", map[string]any{"value": "x"})
	id, _ := dataOf(body)["plan_id"].(string)
	if res.IsError || id == "" || f.writes.Load() != 0 {
		t.Fatalf("preview = %v, writes = %d", body, f.writes.Load())
	}
	if res, body := h.call(t, "value_set", map[string]any{"value": "x", "plan_id": id}); res.IsError || f.writes.Load() != 1 {
		t.Fatalf("confirm = %v, writes = %d", body, f.writes.Load())
	}
}

func TestUIScreensBudget(t *testing.T) {
	total := 0
	for _, screen := range uiScreens {
		b, err := uiFS.ReadFile("ui/" + screen + ".html")
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 40<<10 {
			t.Errorf("%s.html = %d bytes, max 40 KiB", screen, len(b))
		}
		total += len(b)
	}
	if total > 160<<10 {
		t.Errorf("screens total %d bytes, max 160 KiB", total)
	}
	files, _ := fs.Glob(uiFS, "ui/*.html")
	if len(files) != len(uiScreens) {
		t.Errorf("embedded %v, registered %v", files, uiScreens)
	}
}

// Views insert third-party strings with textContent only and load nothing
// from the network.
func TestUIScreensSafeDOM(t *testing.T) {
	forbidden := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function|\bfetch\(|XMLHttpRequest|WebSocket|<script[^>]+src=|<link[^>]+href=|@import|https?://`)
	for _, screen := range uiScreens {
		b, _ := uiFS.ReadFile("ui/" + screen + ".html")
		src := strings.ReplaceAll(string(b), `"http://www.w3.org/2000/svg"`, "")
		if m := forbidden.FindString(src); m != "" {
			t.Errorf("%s.html uses %q", screen, m)
		}
		for _, need := range []string{"ui/initialize", "ui/notifications/initialized", "ui/notifications/tool-input", "ui/notifications/tool-result", "ui/notifications/size-changed", "textContent"} {
			if !strings.Contains(src, need) {
				t.Errorf("%s.html lacks %s", screen, need)
			}
		}
	}
}
