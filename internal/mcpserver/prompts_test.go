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
