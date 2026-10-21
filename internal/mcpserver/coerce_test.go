package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // real registry
)

// TestCoerceOverServer drives the real apps_list through tools/call, the path
// MCP clients take: a stringified integer is coerced, inexact ones are not.
func TestCoerceOverServer(t *testing.T) {
	k := tooltest.New(t)
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[{"type":"apps","id":"1","attributes":{"name":"Hjul","bundleId":"com.idoo.hjul","sku":"HJUL"}}],"links":{"self":"https://api.appstoreconnect.apple.com/v1/apps"}}`)
	h := connect(t, Deps{Env: *k.Env, Profile: k.Env.Profile}, "t", nil)
	if res, _ := h.call(t, "apps_list", json.RawMessage(`{"limit":"3"}`)); res.IsError {
		t.Fatalf(`limit "3" rejected: %+v`, res.Content)
	}
	for _, args := range []string{`{"limit":"3.5"}`, `{"limit":"three"}`} {
		res, body := h.call(t, "apps_list", json.RawMessage(args))
		b, _ := json.Marshal(body)
		if !res.IsError || !strings.Contains(string(b), "invalid_arguments") {
			t.Errorf("%s: want invalid_arguments, got %s", args, b)
		}
	}
}
