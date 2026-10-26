package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Every result carries _meta as_of with UTC seconds and the
// America/Los_Angeles calendar day, from the session clock.
func TestRenderAsOf(t *testing.T) {
	at := time.Date(2026, 10, 27, 3, 30, 15, 999, time.UTC) // 26th in Cupertino
	s := &session{deps: Deps{MaxResultBytes: 4096, Now: func() time.Time { return at }}}
	for _, r := range []*tool.Result{tool.OK(map[string]any{"x": 1}), tool.Fail(tool.ErrInvalidArgs)} {
		var body struct {
			Meta map[string]struct {
				AsOf map[string]string `json:"as_of"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(s.render(r).StructuredContent.(json.RawMessage), &body); err != nil {
			t.Fatal(err)
		}
		got := body.Meta["app-store-connect-mcp"].AsOf
		if got["utc"] != "2026-10-27T03:30:15Z" || got["apple_day"] != "2026-10-26" {
			t.Errorf("as_of = %v", got)
		}
	}
}

func TestRenderErrorNextOnce(t *testing.T) {
	s := &session{deps: Deps{MaxResultBytes: 4096}}
	err := tool.WithNext(&tool.ErrNotFound{Kind: "version", Ref: "1.0"}, tool.NextStep{Tool: "release_prepare"})
	for _, r := range []*tool.Result{tool.Fail(err), {Data: tool.BodyFor(err), IsError: true}} {
		text := s.render(r).Content[0].(*mcp.TextContent).Text
		if n := strings.Count(text, `"next"`); n != 1 || !strings.Contains(text, `"next":[{"tool":"release_prepare"`) {
			t.Errorf("next emitted %d times: %s", n, text)
		}
	}
}

// R7 AO4: translate_metadata steers agents to per-locale work and to the
// export_dir → fastlane_dir round trip instead of one giant argument.
func TestTranslatePromptGuidance(t *testing.T) {
	for _, p := range prompts {
		if p.name != "translate_metadata" {
			continue
		}
		for _, want := range []string{"locale by locale", "metadata_pull export_dir", "metadata_plan as fastlane_dir"} {
			if !strings.Contains(p.text, want) {
				t.Errorf("prompt lacks %q", want)
			}
		}
		return
	}
	t.Fatal("translate_metadata prompt missing")
}
