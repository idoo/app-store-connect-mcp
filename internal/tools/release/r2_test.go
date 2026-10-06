package release

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// ReleaseDay-1: latest-valid stays VALID-only once pinned to the version.
func TestVersionedBuildRef(t *testing.T) {
	tests := []struct {
		ref     tool.BuildRef
		version string
		want    tool.BuildRef
	}{
		{"latest", "1.4", "latest:1.4"},
		{"LATEST", "1.4", "latest:1.4"},
		{"latest-valid", "1.4", "latest-valid:1.4"},
		{" Latest-Valid ", "1.4", "latest-valid:1.4"},
		{"latest-valid", "", "latest-valid"},
		{"42", "1.4", "42"},
		{"b-uuid", "1.4", "b-uuid"},
	}
	for _, tt := range tests {
		if got := versionedBuildRef(tt.ref, tt.version); got != tt.want {
			t.Errorf("versionedBuildRef(%q, %q) = %q, want %q", tt.ref, tt.version, got, tt.want)
		}
	}
}

// ReleaseDay-2: an unresolvable desired build is a block check, not a failure.
func TestReleaseStatusDesiredBuildNotFound(t *testing.T) {
	k := setup(t, readyFixture())
	plain, _ := call(t, setup(t, readyFixture()), `{"app":"Hjul"}`)
	out, res := call(t, k, `{"app":"Hjul","desired":{"build":"latest-valid","release_type":"MANUAL"}}`)
	if len(out.Checks) != len(plain.Checks)+1 {
		t.Fatalf("checks = %d, want %d", len(out.Checks), len(plain.Checks)+1)
	}
	c := checkByID(t, out, "desired_build")
	if c.Status != statusBlock || c.FixVia == nil || c.FixVia.Tool != "builds_wait" ||
		c.FixVia.Args["app"] != "100" || c.FixVia.Args["platform"] != "IOS" || c.FixVia.Args["build"] != "latest-valid" {
		t.Fatalf("desired_build = %+v", c)
	}
	if ready(out) || out.Summary.Block != 1 {
		t.Errorf("summary = %+v", out.Summary)
	}
	if out.Plan == nil || slices.ContainsFunc(out.Plan.Changes, func(s string) bool { return strings.HasPrefix(s, "attach build") }) {
		t.Fatalf("plan = %+v", out.Plan)
	}
	var tools []string
	for _, n := range res.Next {
		tools = append(tools, n.Tool)
		if n.Tool == "release_prepare" && n.Args["build"] != nil {
			t.Errorf("release_prepare keeps the unresolved build: %+v", n.Args)
		}
	}
	if !slices.Contains(tools, "builds_wait") || !slices.Contains(tools, "release_prepare") {
		t.Errorf("next = %v", tools)
	}
}

// ReleaseDay-6: the listing check names its first problem and carries args.
func TestReleaseStatusListingInlinesFirstProblem(t *testing.T) {
	f := readyFixture()
	f.subtitle = strings.Repeat("s", 44)
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	c := checkByID(t, out, "listing")
	if c.Status != statusBlock || !strings.HasPrefix(c.Detail, "1 listing problem(s); first: de-DE: subtitle") {
		t.Fatalf("listing = %+v", c)
	}
	if c.FixVia == nil || c.FixVia.Tool != "metadata_plan" || c.FixVia.Args["app"] != "100" || c.FixVia.Args["platform"] != "IOS" {
		t.Errorf("fix_via = %+v", c.FixVia)
	}
}

// AppleAPIExpert-2: phased release is planned and applied only for updates.
func TestReleasePreparePhasedFirstVersionSkipped(t *testing.T) {
	f := readyFixture()
	f.first = true
	k := setup(t, f)
	out, sres := call(t, k, `{"desired":{"release_type":"MANUAL","phased":true}}`)
	if slices.ContainsFunc(out.Plan.Changes, func(s string) bool { return strings.Contains(s, "phased") }) {
		t.Errorf("first-version plan enables phased: %v", out.Plan.Changes)
	}
	if !slices.ContainsFunc(out.Plan.Notes, func(s string) bool { return strings.Contains(s, "only available for updates") }) {
		t.Errorf("notes = %v", out.Plan.Notes)
	}
	if len(sres.Next) == 0 || sres.Next[0].Tool != "release_prepare" {
		t.Fatalf("status next = %+v", sres.Next)
	}
	writable(k)
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}", 200, doc(t, jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"releaseType": "MANUAL"}}))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args)))
	po := tooltest.Decode[prepareOut](t, res)
	i := slices.IndexFunc(po.Steps, func(s prepStep) bool { return s.Step == "phased_release" })
	if i < 0 || po.Steps[i].Status != stepSkipped || po.Steps[i].Detail != "phased release is only available for updates" {
		t.Errorf("steps = %+v", po.Steps)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/appStoreVersionPhasedReleases")); n != 0 {
		t.Errorf("phased POSTs = %d, want 0", n)
	}
}

// SRE-2: a product list cut at the item cap warns instead of claiming ready.
func TestReleaseStatusProductListTruncated(t *testing.T) {
	k := setup(t, readyFixture())
	items := make([]jres, 5000)
	for i := range items {
		items[i] = jres{Type: "inAppPurchases", ID: fmt.Sprintf("iap%d", i), Attrs: map[string]any{"name": fmt.Sprintf("P%d", i), "state": "APPROVED"}}
	}
	page := strings.TrimSuffix(doc(t, items), "}") + `,"links":{"next":"/v1/apps/100/inAppPurchasesV2?cursor=p2"}}`
	k.ASC.HandleFunc("GET", "/v1/apps/{id}/inAppPurchasesV2", func(tool.Request) tooltest.Fixture {
		return tooltest.Fixture{Status: 200, Body: []byte(page)}
	})
	out, _ := call(t, k, `{"app":"Hjul"}`)
	c := checkByID(t, out, "in_app_products")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "in-app purchase list truncated at 5000; first-time product check is partial") {
		t.Fatalf("in_app_products = %+v", c)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/{id}/inAppPurchasesV2")); n != 1 {
		t.Errorf("IAP pages = %d, want 1 (cap 5000)", n)
	}
}

// ReleaseDay-3: an inferred platform with no builds names the other
// platforms that have some.
func TestBuildsListOtherPlatforms(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "se.jvcr.hjul", Platforms: []tool.Platform{tool.IOS, tool.MacOS}}}
	k.ASC.HandleFunc("GET", "/v1/builds", func(r tool.Request) tooltest.Fixture {
		if r.Query.Get("filter[preReleaseVersion.platform]") == "MAC_OS" {
			if r.Query.Get("limit") != "1" {
				t.Errorf("probe limit = %q", r.Query.Get("limit"))
			}
			return tooltest.Fixture{Status: 200, Body: []byte(`{"data":[{"type":"builds","id":"m1","attributes":{"version":"7"}}],"meta":{"paging":{"total":13,"limit":1}}}`)}
		}
		return tooltest.Fixture{Status: 200, Body: []byte(`{"data":[]}`)}
	})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_list"), k.Env, `{"app":"Hjul"}`))
	out := tooltest.Decode[buildsListOut](t, res)
	if out.Platform != tool.IOS || out.OtherPlatforms[tool.MacOS] != 13 || len(out.OtherPlatforms) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_list" || res.Next[0].Args["platform"] != "MAC_OS" {
		t.Errorf("next = %+v", res.Next)
	}

	// An explicit platform is never second-guessed.
	before := len(k.ASC.RequestsTo("GET", "/v1/builds"))
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_list"), k.Env, `{"app":"Hjul","platform":"IOS"}`))
	if out := tooltest.Decode[buildsListOut](t, res); out.OtherPlatforms != nil {
		t.Errorf("explicit platform probed: %+v", out.OtherPlatforms)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/builds")) - before; n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}
