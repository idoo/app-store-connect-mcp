package release

import (
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// CD-5: the release_submit approval form shows the build number, version,
// the field names changed per locale since the live version and the primary
// What's New as untrusted text.
func TestReleaseSubmitPreviewListsContent(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	k.Resolve.Versions[tool.VersionLive] = tool.Version{ID: "v0", String: "1.3", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"}
	same := map[string]any{"description": "A sailing app.", "keywords": "boat,sail,wind", "supportUrl": "https://jvcr.se/support", "promotionalText": "Sail on."}
	en := map[string]any{"locale": "en-US", "whatsNew": "Old fixes."}
	de := map[string]any{"locale": "de-DE", "whatsNew": "Bug fixes."}
	for k, v := range same {
		en[k], de[k] = v, v
	}
	k.ASC.Handle("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, doc(t, []jres{
		{Type: "appStoreVersionLocalizations", ID: "old-en", Attrs: en},
		{Type: "appStoreVersionLocalizations", ID: "old-de", Attrs: de},
	}))

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_submit"), k.Env, `{"app":"Hjul","version":"1.4"}`))
	prev := tooltest.Decode[struct {
		PlanID  string `json:"plan_id"`
		Preview struct {
			Intended  []string `json:"intended"`
			Untrusted []string `json:"untrusted"`
		} `json:"preview"`
	}](t, res)
	p := prev.Preview
	for _, want := range []string{"build 42, version 1.4", "en-US: changed whatsNew vs live 1.3", "1 locale(s) unchanged vs live 1.3"} {
		if !slices.Contains(p.Intended, want) {
			t.Errorf("intended %q missing from %q", want, p.Intended)
		}
	}
	if !slices.Equal(p.Untrusted, []string{"What's New (en-US): Bug fixes."}) {
		t.Errorf("untrusted = %q", p.Untrusted)
	}
	got := slices.DeleteFunc(k.ASC.RequestsTo("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations"), func(r tool.Request) bool {
		return r.Query.Get("include") == "placements" // Asset Library discovery, not the live content read
	})
	if len(got) != 1 || !strings.Contains(got[0].Path, "/v0/") {
		t.Errorf("live localization reads = %+v", got)
	}

	asked := k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_submit"), k.Env, `{"app":"Hjul","version":"1.4","plan_id":"`+prev.PlanID+`"}`))
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "build 42, version 1.4") || !strings.Contains((*asked)[0], "changed whatsNew") ||
		!strings.Contains((*asked)[0], "Untrusted third-party text") {
		t.Fatalf("prompt = %q", *asked)
	}
}
