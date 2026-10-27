package tool

import (
	"fmt"
	"strings"
	"testing"
)

// A "which app?" ambiguity from a tool that accepts app:"all" offers it in the
// message and as the first next step (L2-2); other tools and named refs don't.
func TestOfferAllApps(t *testing.T) {
	err := offerAllApps("digest_summary", fmt.Errorf("wrap: %w", &ErrAmbiguous{Kind: "app", Total: 24}))
	if !strings.Contains(err.Error(), `app:"all"`) {
		t.Fatalf("message = %q", err.Error())
	}
	b := BodyFor(err)
	if len(b.Next) == 0 || b.Next[0].Tool != "digest_summary" || b.Next[0].Args["app"] != "all" {
		t.Fatalf("next = %+v", b.Next)
	}
	for _, e := range []error{
		offerAllApps("apps_get", &ErrAmbiguous{Kind: "app", Total: 24}),
		offerAllApps("reviews_list", &ErrAmbiguous{Kind: "app", Ref: "Notes", Total: 2}),
	} {
		if strings.Contains(e.Error(), `"all"`) {
			t.Errorf("unexpected all offer: %q", e.Error())
		}
	}
}
