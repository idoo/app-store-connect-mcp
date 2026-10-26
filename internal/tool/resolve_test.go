package tool_test

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// An app-less call on a multi-app account reports the true app count, not
// the capped candidate list, and names default_app and the project pin.
func TestErrAmbiguousTrueCount(t *testing.T) {
	cands := []tool.Candidate{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}, {ID: "5"}}
	msg := (&tool.ErrAmbiguous{Kind: "app", Candidates: cands, Total: 23}).Error()
	for _, want := range []string{"which app? you have 23 apps", "default_app", ".app-store-connect-mcp.toml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if b := tool.BodyFor(&tool.ErrAmbiguous{Kind: "app", Candidates: cands, Total: 23}); len(b.Candidates) != 5 {
		t.Errorf("candidates = %d, want 5", len(b.Candidates))
	}
	named := (&tool.ErrAmbiguous{Kind: "app", Ref: "Notes", Candidates: cands, Total: 7}).Error()
	if named != `app "Notes" is ambiguous (7 matches); pass an id` {
		t.Errorf("named = %q", named)
	}
	// Without Total the candidate count still applies.
	if m := (&tool.ErrAmbiguous{Kind: "build", Ref: "1", Candidates: cands[:2]}).Error(); !strings.Contains(m, "(2 matches)") {
		t.Errorf("build = %q", m)
	}
}
