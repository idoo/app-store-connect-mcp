package xcodecloud

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// startKit serves the Release workflow with branch main and the given
// response to POST /v1/ciBuildRuns.
func startKit(t *testing.T, post tooltest.Fixture) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, prodDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/workflows", http.StatusOK, workflowsDoc)
	k.ASC.Handle(http.MethodGet, "/v1/scmRepositories/{id}/gitReferences", http.StatusOK, `{"data":[
	 {"type":"scmGitReferences","id":"G1","attributes":{"name":"main","canonicalName":"refs/heads/main","kind":"BRANCH","isDeleted":false}}]}`)
	k.ASC.HandleFixture(http.MethodPost, "/v1/ciBuildRuns", post)
	return k
}

func confirmStart(t *testing.T, k *tooltest.Kit) *tool.Result {
	t.Helper()
	tl := lookup(t, "xcc_build_start")
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"workflow":"release","branch":"main"}`))
	return tooltest.Call(t, tl, k.Env, `{"workflow":"release","branch":"main","confirm":true}`)
}

// A 2xx with no run means Apple started it: ok with a pointer to the run
// list, never an internal error that invites a second build.
func TestBuildStartEmptyResponseIsStarted(t *testing.T) {
	k := startKit(t, tooltest.Fixture{Status: http.StatusCreated})
	res := tooltest.MustOK(t, confirmStart(t, k))
	j := tooltest.JSON(t, res)
	for _, want := range []string{`xcc_build_status`, wfID, "instead of starting another"} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %q in %s", want, j)
		}
	}
	if n := len(k.ASC.Mutations()); n != 1 {
		t.Fatalf("mutations = %d", n)
	}
}

// A timeout or 5xx is outcome_unknown with a step to read the runs first.
func TestBuildStartUnknownOutcomePointsToRuns(t *testing.T) {
	k := startKit(t, tooltest.Fixture{Err: fmt.Errorf("%w: timeout", tool.ErrOutcomeUnknown)})
	res := tooltest.MustFail(t, confirmStart(t, k), tool.ErrOutcomeUnknown.Code)
	if j := tooltest.JSON(t, res); !strings.Contains(j, `xcc_build_status`) || !strings.Contains(j, wfID) {
		t.Fatalf("no run-list step: %s", j)
	}
	if n := len(k.ASC.Mutations()); n != 1 {
		t.Fatalf("mutations = %d", n)
	}
}
