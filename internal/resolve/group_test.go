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

func group(id, name string, internal bool) res {
	return res{Type: "betaGroups", ID: id, Attributes: map[string]any{"name": name, "isInternalGroup": internal}}
}

func asNotFound(err error, nf **tool.ErrNotFound) bool { return errors.As(err, nf) }

func TestGroupRefForms(t *testing.T) {
	tests := []struct {
		ref   tool.GroupRef
		want  tool.Group
		kind  string
		cands int
	}{
		{ref: "g2", want: tool.Group{ID: "g2", Name: "External Testers"}},
		{ref: "external testers", want: tool.Group{ID: "g2", Name: "External Testers"}},
		{ref: " Team ", want: tool.Group{ID: "g1", Name: "Team", Internal: true}},
		{ref: "beta", kind: "ambiguous", cands: 2},
		{ref: "External", kind: "notfound"}, // no prefix matching for groups
		{ref: "", kind: "notfound"},
	}
	for _, tt := range tests {
		t.Run(string(tt.ref), func(t *testing.T) {
			f := tooltest.NewFakeASC(t)
			f.Handle(http.MethodGet, "/v1/apps/a1/betaGroups", 200, body(t,
				group("g1", "Team", true), group("g2", "External Testers", false),
				group("g3", "Beta", false), group("g4", "beta", false)))
			got, err := New(f, Options{}).Group(context.Background(), "a1", tt.ref)
			next, n := wantErr(t, err, tt.kind)
			if tt.kind != "" && next != "testflight_groups_list" {
				t.Fatalf("next = %q", next)
			}
			if n != tt.cands || got != tt.want {
				t.Fatalf("got %+v (candidates %d), want %+v (%d)", got, n, tt.want, tt.cands)
			}
			var nf *tool.ErrNotFound
			if asNotFound(err, &nf) && !strings.Contains(nf.Hint, "External Testers") {
				t.Fatalf("hint %q must list the groups", nf.Hint)
			}
		})
	}
}

func TestGroupNoneHint(t *testing.T) {
	f := tooltest.NewFakeASC(t)
	f.Handle(http.MethodGet, "/v1/apps/a1/betaGroups", 200, body(t))
	_, err := New(f, Options{}).Group(context.Background(), "a1", "x")
	var nf *tool.ErrNotFound
	if !asNotFound(err, &nf) || nf.Hint != "the app has no beta groups" {
		t.Fatalf("got %v", err)
	}
}
