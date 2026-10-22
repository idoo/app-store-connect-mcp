package screenshots

import (
	"os"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// Apple's ref data allows alpha for Mac screenshot groups, not iPhone ones.
func TestPlanAlphaFollowsRefData(t *testing.T) {
	ref, err := os.ReadFile("../assetlib/testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	k, _ := newKit(t)
	k.Env.Profile.Name = "alpha-refdata" // LoadRefData caches per profile
	k.ASC.Handle("GET", "/v1/appAssetLibraryRefData", 200, string(ref))
	write(t, k, "shots/en-US/mac/01.png", pngImage(t, 2880, 1800, true, 1))
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, true, 2))
	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	var macBad, phoneBad bool
	for _, pr := range p.Problems {
		if pr.Kind == problemInvalidImage {
			macBad = macBad || pr.File == "en-US/mac/01.png"
			phoneBad = phoneBad || pr.File == "en-US/01.png"
		}
	}
	if macBad || !phoneBad {
		t.Errorf("mac alpha rejected=%v, iPhone alpha rejected=%v: %+v", macBad, phoneBad, p.Problems)
	}
}
