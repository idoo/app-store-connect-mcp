package resolve

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// PO4: app:"all" is reserved. Tools that work on one app get invalid_args
// naming the tools that accept it, never not_found, and Apple is not asked.
func TestAppAllIsInvalidArgs(t *testing.T) {
	f := tooltest.NewFakeASC(t)
	f.Handle(http.MethodGet, "/v1/apps", 200, body(t, app("100", "All", "se.jvcr.all", "")))
	for _, ref := range []tool.AppRef{"all", " ALL "} {
		_, err := New(f, Options{}).App(context.Background(), ref)
		if !errors.Is(err, tool.ErrInvalidArgs) || tool.IsNotFound(err) {
			t.Fatalf("%q: err = %v, want invalid_args", ref, err)
		}
		if msg := err.Error(); !strings.Contains(msg, `app "all" works only in reviews_list, digest_summary, insights_summary`) {
			t.Fatalf("%q: message = %q", ref, msg)
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}
