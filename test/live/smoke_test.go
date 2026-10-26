//go:build live

// Package live is a read-only smoke test against the real App Store Connect
// API. It uses the credentials asc-mcp would use (ASC_* env, ASC_MCP_PROFILE or the
// config file), forces read-only mode and refuses every non-GET request at
// the HTTP transport, so a bug can at worst fail the test, never touch Apple.
//
//	make smoke
//	ASC_MCP_SMOKE_APP=1234567890 make smoke   # pick the app (default: first)
package live

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all"
)

// getOnly refuses and records every request that is not a GET.
type getOnly struct {
	next http.RoundTripper

	mu      sync.Mutex
	refused []string
}

func (g *getOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		g.mu.Lock()
		g.refused = append(g.refused, req.Method+" "+req.URL.Path)
		g.mu.Unlock()
		return nil, fmt.Errorf("live smoke: refusing %s %s (GET only)", req.Method, req.URL.Path)
	}
	return g.next.RoundTrip(req)
}

func (g *getOnly) violations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refused...)
}

func TestSmoke(t *testing.T) {
	// The ASC client uses an http.Client without a Transport, so it goes
	// through http.DefaultTransport; the guard sits in front of it.
	guard := &getOnly{next: http.DefaultTransport}
	http.DefaultTransport = guard
	t.Cleanup(func() {
		http.DefaultTransport = guard.next
		if v := guard.violations(); len(v) > 0 {
			t.Errorf("non-GET requests attempted: %v", v)
		}
	})

	readOnly := false
	r, err := config.Resolve(config.Options{Flags: config.Flags{AllowWrites: &readOnly}})
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if r.Profile.KeyID == "" {
		t.Skip("no App Store Connect credentials (set ASC_KEY_ID or configure a profile)")
	}
	env, err := app.NewEnv(context.Background(), r, "live-smoke")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if env.Profile.AllowWrites {
		t.Fatal("env is not read-only")
	}

	call := func(t *testing.T, name, args string) *tool.Result {
		t.Helper()
		tl, ok := tool.Lookup(name)
		if !ok {
			t.Fatalf("tool %s is not registered", name)
		}
		return tooltest.MustOK(t, tooltest.Call(t, tl, env, args))
	}

	t.Run("doctor", func(t *testing.T) { call(t, "doctor", "") })

	appID := os.Getenv("ASC_MCP_SMOKE_APP")
	t.Run("apps_list", func(t *testing.T) {
		res := call(t, "apps_list", `{"limit":5}`)
		out := tooltest.Decode[struct {
			Apps []struct {
				ID string `json:"id"`
			} `json:"apps"`
		}](t, res)
		if len(out.Apps) == 0 {
			t.Fatal("apps_list returned no apps")
		}
		if appID == "" {
			appID = out.Apps[0].ID
		}
	})
	if appID == "" {
		t.Fatal("no app to test against")
	}
	appArg := fmt.Sprintf(`{"app":%q}`, appID)

	t.Run("apps_get", func(t *testing.T) { call(t, "apps_get", appArg) })
	t.Run("builds_list", func(t *testing.T) { call(t, "builds_list", fmt.Sprintf(`{"app":%q,"limit":5}`, appID)) })
	t.Run("release_status", func(t *testing.T) { call(t, "release_status", appArg) })
	t.Run("reviews_list", func(t *testing.T) {
		// mark_seen=false keeps the local since-cursor untouched.
		call(t, "reviews_list", fmt.Sprintf(`{"app":%q,"limit":5,"mark_seen":false}`, appID))
	})
	t.Run("testflight_groups_list", func(t *testing.T) { call(t, "testflight_groups_list", appArg) })
	t.Run("asc_api_search", func(t *testing.T) { call(t, "asc_api_search", `{"query":"list apps","limit":3}`) })
	t.Run("asc_api_get", func(t *testing.T) {
		call(t, "asc_api_get", `{"path":"/v1/apps","query":{"limit":"1"},"limit":1}`)
	})
	t.Run("sales_report", func(t *testing.T) {
		if r.Profile.VendorNumber == "" {
			t.Skip("no vendor number (ASC_VENDOR_NUMBER or vendor_number in the profile)")
		}
		call(t, "sales_report", "")
	})
}
