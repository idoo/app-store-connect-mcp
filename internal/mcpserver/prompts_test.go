package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTranslateMetadataPromptPlatform(t *testing.T) {
	var p promptDef
	for _, d := range prompts {
		if d.name == "translate_metadata" {
			p = d
		}
	}
	text := func(args map[string]string) string {
		t.Helper()
		res, err := p.handle(context.Background(), &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Name: p.name, Arguments: args}})
		if err != nil {
			t.Fatalf("handle: %v", err)
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text
	}
	base := map[string]string{"app": "Lesoma", "from_locale": "en-US", "to_locales": "de-DE"}
	if got := text(base); !strings.Contains(got, "platform: the app's main platform") || !strings.Contains(got, "repeat for each platform") {
		t.Errorf("default text = %q", got)
	}
	base["platform"] = "MAC_OS"
	if got := text(base); !strings.Contains(got, "platform: MAC_OS") {
		t.Errorf("platform text = %q", got)
	}
}

func TestDigestAndPostReleasePrompts(t *testing.T) {
	get := func(name string, args map[string]string) string {
		t.Helper()
		for _, d := range prompts {
			if d.name == name {
				res, err := d.handle(context.Background(), &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Name: name, Arguments: args}})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return res.Messages[0].Content.(*mcp.TextContent).Text
			}
		}
		t.Fatalf("prompt %s missing", name)
		return ""
	}
	d := get("daily_digest", map[string]string{})
	for _, want := range []string{"every app", "digest_summary", "without mark_seen", "Only after I say I have read it", "mark_seen:true"} {
		if !strings.Contains(d, want) {
			t.Errorf("daily_digest lacks %q: %s", want, d)
		}
	}
	if got := get("daily_digest", map[string]string{"app": "Hjul"}); !strings.Contains(got, "for Hjul") {
		t.Errorf("daily_digest app = %s", got)
	}
	p := get("post_release_check", map[string]string{"app": "Hjul", "platform": "MAC_OS"})
	for _, want := range []string{"Hjul (platform: MAC_OS)", "release_health_report", "never pause without my explicit approval", "analytics_funnel_report", "reviews_list"} {
		if !strings.Contains(p, want) {
			t.Errorf("post_release_check lacks %q: %s", want, p)
		}
	}
}
