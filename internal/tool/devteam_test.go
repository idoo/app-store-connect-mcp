package tool_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestResolveDevTeamArgument(t *testing.T) {
	k := tooltest.New(t) // no route: an argument must never read the API
	got, err := tool.ResolveDevTeam(context.Background(), k.Env, " ABCDE12345 ")
	if err != nil || got != "ABCDE12345" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"abcde12345", "ABCDE1234", "ABCDE123456", "ABCDE-2345"} {
		_, err := tool.ResolveDevTeam(context.Background(), k.Env, bad)
		if !errors.Is(err, tool.ErrInvalidArgs) || !strings.Contains(err.Error(), "team_id") {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

func TestResolveDevTeamReadsSeed(t *testing.T) {
	k := tooltest.New(t)
	k.ASC.Handle("GET", "/v1/bundleIds", 200, `{"data":[{"type":"bundleIds","id":"B1","attributes":{"identifier":"com.example.app","seedId":"QWERTY1234"}}]}`)
	got, err := tool.ResolveDevTeam(context.Background(), k.Env, "")
	if err != nil || got != "QWERTY1234" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveDevTeamCannotTell(t *testing.T) {
	for name, body := range map[string]string{
		"no bundles": `{"data":[]}`,
		"bad seed":   `{"data":[{"type":"bundleIds","id":"B1","attributes":{"identifier":"com.example.app","seedId":""}}]}`,
	} {
		k := tooltest.New(t)
		k.ASC.Handle("GET", "/v1/bundleIds", 200, body)
		_, err := tool.ResolveDevTeam(context.Background(), k.Env, "")
		if !errors.Is(err, tool.ErrInvalidArgs) || !strings.Contains(err.Error(), "team_id") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	k := tooltest.New(t)
	k.ASC.Handle("GET", "/v1/bundleIds", 500, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR"}]}`)
	if _, err := tool.ResolveDevTeam(context.Background(), k.Env, ""); err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Errorf("api failure: err = %v", err)
	}
}
