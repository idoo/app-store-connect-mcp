package release

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// multiPlatformKit is a read-only kit for an iOS + macOS app.
func multiPlatformKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t).ReadOnly()
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "se.jvcr.hjul", Platforms: []tool.Platform{tool.IOS, tool.MacOS}}}
	return k
}

// An inferred platform must not hide another platform's rejected submission.
func TestReleaseReviewStatusListsOtherPlatforms(t *testing.T) {
	k := multiPlatformKit(t)
	k.ASC.HandleFunc("GET", "/v1/reviewSubmissions", func(r tool.Request) tooltest.Fixture {
		if r.Query.Get("filter[platform]") == "IOS" {
			return tooltest.Fixture{Status: 200, Body: []byte(`{"data":[]}`)}
		}
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, []jres{
			{Type: "reviewSubmissions", ID: "old", Attrs: map[string]any{"platform": "MAC_OS", "state": "COMPLETE", "submittedDate": "2026-01-01T00:00:00Z"}},
			{Type: "reviewSubmissions", ID: "d345", Attrs: map[string]any{"platform": "MAC_OS", "state": "UNRESOLVED_ISSUES", "submittedDate": "2026-02-01T00:00:00Z"}, Rels: map[string]any{"appStoreVersionForReview": "appStoreVersions:mv"}},
		}, jres{Type: "appStoreVersions", ID: "mv", Attrs: map[string]any{"versionString": "3.6", "appVersionState": "REJECTED"}}))}
	})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{"app":"Hjul"}`))
	out := tooltest.Decode[reviewStatusOut](t, res)
	want := []otherSubmission{{Platform: "MAC_OS", Submission: "d345", State: "UNRESOLVED_ISSUES", Version: "3.6", VersionState: "REJECTED"}}
	if out.Platform != tool.IOS || !slices.Equal(out.OtherPlatforms, want) {
		t.Fatalf("other platforms = %+v", out.OtherPlatforms)
	}
	if !slices.ContainsFunc(res.Next, func(n tool.NextStep) bool {
		return n.Tool == "release_review_status" && n.Args["platform"] == "MAC_OS"
	}) {
		t.Errorf("next = %+v", res.Next)
	}

	// An explicit platform costs no extra request.
	before := len(k.ASC.RequestsTo("GET", "/v1/reviewSubmissions"))
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{"app":"Hjul","platform":"IOS"}`))
	if out := tooltest.Decode[reviewStatusOut](t, res); out.OtherPlatforms != nil || len(k.ASC.RequestsTo("GET", "/v1/reviewSubmissions"))-before != 1 {
		t.Errorf("explicit platform probed others: %+v", out.OtherPlatforms)
	}
}

// platformResolver resolves builds per platform (the fake ignores platform).
type platformResolver struct {
	*tooltest.FakeResolver
	builds map[tool.Platform]map[tool.BuildRef]tool.Build
}

func (r platformResolver) Build(_ context.Context, _ string, p tool.Platform, ref tool.BuildRef) (tool.Build, error) {
	if b, ok := r.builds[p][ref]; ok {
		return b, nil
	}
	return tool.Build{}, &tool.ErrNotFound{Kind: "build", Ref: string(ref), Hint: "for " + string(p)}
}

// builds_wait without platform finds a build on the app's other platform,
// like builds_list; a miss names every platform searched.
func TestBuildsWaitFallsBackAcrossPlatforms(t *testing.T) {
	k := multiPlatformKit(t)
	k.Env.Resolve = platformResolver{FakeResolver: k.Resolve, builds: map[tool.Platform]map[tool.BuildRef]tool.Build{
		tool.MacOS: {"65": {ID: "m65", Number: "65"}},
	}}
	k.ASC.Handle("GET", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "m65", Attrs: map[string]any{"version": "65", "processingState": "VALID"}}))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"app":"Hjul","build":"65"}`))
	out := tooltest.Decode[buildsWaitOut](t, res)
	if out.BuildID != "m65" || out.Platform != tool.MacOS || out.PlatformFallback == nil || out.PlatformFallback.From != tool.IOS {
		t.Fatalf("out = %+v", out)
	}

	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"app":"Hjul","build":"99"}`), "not_found")
	if !strings.Contains(body.Hint, "IOS, MAC_OS") {
		t.Errorf("hint = %q", body.Hint)
	}
	// An explicit platform does not fall back.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"app":"Hjul","platform":"IOS","build":"65"}`), "not_found")
}
