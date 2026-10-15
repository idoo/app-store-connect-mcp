package core

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A replayed DELETE that finds the object gone already applied: success with
// already:true. A first-attempt 404 stays an error.
func TestAPIRequestDeleteReplayGone(t *testing.T) {
	gone := fmt.Errorf("%w: %w", tool.ErrAlreadyGone, &tool.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND"})
	args := `{"method":"DELETE","path":"/v1/betaTesters/t1","confirm":true}`

	k := kit(t)
	k.Elicit(true)
	k.ASC.HandleFixture(http.MethodDelete, "/v1/betaTesters/{id}", tooltest.Fixture{Err: gone})
	out := data(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_request"), k.Env, args)))
	if out["already"] != true || out["path"] != "/v1/betaTesters/t1" {
		t.Fatalf("out = %v", out)
	}

	k2 := kit(t)
	k2.Elicit(true)
	k2.ASC.Handle(http.MethodDelete, "/v1/betaTesters/{id}", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	if res := tooltest.Call(t, mustTool(t, "asc_api_request"), k2.Env, args); !res.IsError {
		t.Fatalf("first-attempt 404 = %s", tooltest.JSON(t, res.Data))
	}
}
