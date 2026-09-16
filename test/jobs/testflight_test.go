package jobs

import (
	"net/http"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

// Job 2: "push the latest build to my external testers" in at most 2 calls
// (an external group needs a preview, then a human-approved confirm),
// referring to the app by name and the build as latest.
func TestJobPushLatestBuildToExternalGroup(t *testing.T) {
	k := newKit(t)
	k.Resolve.Builds = map[tool.BuildRef]tool.Build{
		tool.BuildLatest: {ID: "b1", Number: "42", ProcessingState: "VALID"},
	}
	asc := k.ASC
	asc.Handle(http.MethodGet, "/v1/apps/{id}/betaGroups", http.StatusOK, `{"data":[
		{"type":"betaGroups","id":"g-int","attributes":{"name":"Team","isInternalGroup":true}},
		{"type":"betaGroups","id":"g-ext","attributes":{"name":"External Testers","isInternalGroup":false}}
	],"links":{}}`)
	asc.Handle(http.MethodGet, "/v1/builds/{id}", http.StatusOK, `{"data":{"type":"builds","id":"b1",
		"attributes":{"version":"42","processingState":"VALID","expired":false,"usesNonExemptEncryption":false},
		"relationships":{"betaGroups":{"data":[]},"betaBuildLocalizations":{"data":[]},
		"buildBetaDetail":{"data":{"type":"buildBetaDetails","id":"bbd1"}}}},
		"included":[{"type":"buildBetaDetails","id":"bbd1","attributes":{"externalBuildState":"READY_FOR_BETA_SUBMISSION"}}]}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}", http.StatusOK, `{"data":{"type":"apps","id":"app1","attributes":{"primaryLocale":"en-US"}}}`)
	asc.Handle(http.MethodPost, "/v1/betaBuildLocalizations", http.StatusCreated,
		`{"data":{"type":"betaBuildLocalizations","id":"l-en","attributes":{"locale":"en-US"}}}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}/betaAppLocalizations", http.StatusOK,
		`{"data":[{"type":"betaAppLocalizations","id":"bal-en","attributes":{"locale":"en-US","feedbackEmail":"beta@example.com","description":"Hjul"}}],"links":{}}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}/betaAppReviewDetail", http.StatusOK,
		`{"data":{"type":"betaAppReviewDetails","id":"rd1","attributes":{"contactEmail":"ivan@example.com","contactPhone":"+46700000000","contactFirstName":"Ivan","contactLastName":"V","demoAccountRequired":false}}}`)
	asc.Handle(http.MethodPatch, "/v1/betaAppReviewDetails/{id}", http.StatusOK, `{"data":{"type":"betaAppReviewDetails","id":"rd1","attributes":{}}}`)
	asc.Handle(http.MethodPost, "/v1/betaGroups/{id}/relationships/builds", http.StatusNoContent, "")
	asc.Handle(http.MethodPost, "/v1/betaAppReviewSubmissions", http.StatusCreated,
		`{"data":{"type":"betaAppReviewSubmissions","id":"sub1","attributes":{"betaReviewState":"WAITING_FOR_REVIEW"}}}`)

	j := connect(t, k)
	prev := j.call("testflight_distribute", `{"app":"Hjul","groups":["External Testers"],"what_to_test":{"*":"New maps"}}`)
	if prev["status"] != "needs_confirm" || len(asc.Mutations()) != 0 {
		t.Fatalf("external distribute without confirm = %v, mutations %v", prev, asc.Mutations())
	}
	j.call("testflight_distribute", `{"app":"Hjul","groups":["External Testers"],"what_to_test":{"*":"New maps"},"confirm":true}`)
	j.done(2)

	added := false
	for _, m := range asc.Mutations() {
		added = added || (m.Method == http.MethodPost && m.Path == "/v1/betaGroups/g-ext/relationships/builds")
	}
	if !added {
		t.Fatalf("build not added to the external group: %v", asc.Mutations())
	}
}
