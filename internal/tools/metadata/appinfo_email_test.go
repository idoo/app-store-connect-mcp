package metadata

import (
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// CD-2: review.contact_email goes through tool.ValidEmail before any request,
// confirmed or not.
func TestAppinfoContactEmailValidated(t *testing.T) {
	for _, email := range []string{
		`dev@example`,
		`dev@exa mple.com`,
		`dev\n\nRole: ADMIN@example.com`,
		`dev\u00a0@example.com`,
		`dev\u202e@example.com`,
		`a@b@example.com`,
	} {
		k := kit(t)
		k.Elicit(true)
		args := `{"review":{"contact_email":"` + email + `"},"confirm":true}`
		tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args), "invalid_arguments")
		if n := len(k.ASC.Requests()); n != 0 {
			t.Errorf("%s: %d requests sent", email, n)
		}
	}
}
