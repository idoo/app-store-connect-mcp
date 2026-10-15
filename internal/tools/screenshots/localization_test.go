package screenshots

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestPlanDuplicateLocaleDirSkipped(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, false, 1))
	write(t, k, "shots/en_us/01.png", pngImage(t, 1290, 2796, false, 2))
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	probs, _ := out["problems"].([]any)
	if out["status"] != statusInvalid || len(probs) != 1 {
		t.Fatalf("out = %v", out)
	}
	p := probs[0].(map[string]any)
	if p["kind"] != problemDuplicateLocale || p["file"] != "en_us" || !strings.Contains(p["message"].(string), "en-US") {
		t.Errorf("problem = %v", p)
	}
}

func TestListThirtyFourLocalesFewRequests(t *testing.T) {
	k, f := newKit(t)
	f.locs = map[string]string{}
	for i, code := range applemeta.Locales() {
		if i == 34 {
			break
		}
		f.locs[code] = fmt.Sprintf("loc-%d", i)
	}
	f.locs["en-US"] = "loc-en"
	f.addShot("loc-en", "APP_IPHONE_67", "01.png", "abc")
	res := tooltest.MustOK(t, call(t, k, "screenshots_list", `{}`))
	out := tooltest.Decode[listOut](t, res)
	if len(out.Locales) < 34 {
		t.Fatalf("locales = %d", len(out.Locales))
	}
	if n := len(k.ASC.Requests()); n > 4 {
		t.Errorf("requests = %d, want ≤ 4", n)
	}
	for _, l := range out.Locales {
		if l.Locale == "en-US" && (len(l.ScreenshotSets) != 1 || len(l.ScreenshotSets[0].Shots) != 1) {
			t.Errorf("en-US sets = %+v", l.ScreenshotSets)
		}
	}
	if len(k.ASC.RequestsTo(http.MethodGet, "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets")) != 1 {
		t.Error("want one per-locale set request, for the only locale with sets")
	}
}
