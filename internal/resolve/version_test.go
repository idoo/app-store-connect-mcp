package resolve

import (
	"context"
	"net/http"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

func versionsFake(t *testing.T) *tooltest.FakeASC {
	t.Helper()
	f := tooltest.NewFakeASC(t)
	serveVersions(t, f, map[string][]res{
		"ios": {
			ver("old", "IOS", "1.9", "REPLACED_WITH_NEW_VERSION", "2025-06-01T00:00:00-07:00"),
			ver("live", "IOS", "2.0", "READY_FOR_DISTRIBUTION", "2026-01-01T00:00:00-08:00"),
			ver("edit", "IOS", "2.1", "PREPARE_FOR_SUBMISSION", "2026-02-01T00:00:00-08:00"),
		},
		"bumped": {
			ver("live", "IOS", "3.4", "READY_FOR_DISTRIBUTION", "2026-01-01T00:00:00-08:00"),
			ver("patch", "IOS", "3.4.1", "PREPARE_FOR_SUBMISSION", "2026-02-01T00:00:00-08:00"),
		},
		"review": {
			ver("live", "IOS", "1.0", "READY_FOR_DISTRIBUTION", "2026-01-01T00:00:00-08:00"),
			ver("wfr", "IOS", "1.1", "WAITING_FOR_REVIEW", "2026-02-01T00:00:00-08:00"),
		},
		"multi": {
			ver("i1", "IOS", "1.0", "READY_FOR_DISTRIBUTION", ""),
			ver("m1", "MAC_OS", "1.0", "PREPARE_FOR_SUBMISSION", ""),
		},
		"legacy": {{Type: "appStoreVersions", ID: "sale", Attributes: map[string]any{
			"platform": "IOS", "versionString": "4.0", "appStoreState": "READY_FOR_SALE",
		}}},
	})
	f.Handle(http.MethodGet, "/v1/appStoreVersions/abc-123", 200, single(t, ver("abc-123", "IOS", "2.1", "PREPARE_FOR_SUBMISSION", "")))
	f.Handle(http.MethodGet, "/v1/appStoreVersions/missing-id", 404, `{"errors":[{"status":"404"}]}`)
	return f
}

func TestVersionRefForms(t *testing.T) {
	tests := []struct {
		name     string
		app      string
		platform tool.Platform
		ref      tool.VersionRef
		want     tool.Version
		kind     string
		next     string
	}{
		{name: "default", app: "ios", want: tool.Version{ID: "edit", String: "2.1", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"}},
		{name: "editable", app: "ios", ref: "Editable", want: tool.Version{ID: "edit", String: "2.1", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"}},
		{name: "live", app: "ios", ref: "live", want: tool.Version{ID: "live", String: "2.0", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"}},
		{name: "next new", app: "ios", ref: "next", want: tool.Version{String: "2.0.1", Platform: tool.IOS}},
		{name: "next exists", app: "bumped", ref: "next", want: tool.Version{ID: "patch", String: "3.4.1", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"}},
		{name: "exact", app: "ios", ref: "2.0", want: tool.Version{ID: "live", String: "2.0", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"}},
		{name: "exact missing", app: "ios", ref: "9.9", kind: "notfound", next: "release_prepare"},
		{name: "id", app: "ios", ref: "abc-123", want: tool.Version{ID: "abc-123", String: "2.1", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"}},
		{name: "id missing", app: "ios", ref: "missing-id", kind: "notfound", next: "apps_get"},
		{name: "in review", app: "review", kind: "notfound", next: "release_review_status"},
		{name: "no versions editable", app: "empty", kind: "notfound", next: "release_prepare"},
		{name: "no versions live", app: "empty", ref: "live", kind: "notfound", next: "apps_get"},
		{name: "no live next", app: "empty", ref: "next", kind: "notfound", next: "release_prepare"},
		{name: "platform ambiguous", app: "multi", kind: "ambiguous", next: "apps_get"},
		{name: "platform explicit", app: "multi", platform: tool.MacOS, want: tool.Version{ID: "m1", String: "1.0", Platform: tool.MacOS, State: "PREPARE_FOR_SUBMISSION"}},
		{name: "legacy state", app: "legacy", ref: "live", want: tool.Version{ID: "sale", String: "4.0", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(versionsFake(t), Options{Clock: tooltest.NewFakeClock()})
			got, err := r.Version(context.Background(), tt.app, tt.platform, tt.ref)
			if next, _ := wantErr(t, err, tt.kind); next != tt.next {
				t.Fatalf("next = %q, want %q", next, tt.next)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVersionPlatformAmbiguityCandidates(t *testing.T) {
	_, err := New(versionsFake(t), Options{}).Version(context.Background(), "multi", "", "")
	if _, n := wantErr(t, err, "ambiguous"); n != 2 {
		t.Fatalf("candidates = %d, want 2", n)
	}
}

func TestPatchBump(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2", "2.0.1"}, {"2.1", "2.1.1"}, {"2.1.9", "2.1.10"}, {"1.2.3.4", "1.2.3.5"}, {"beta", ""},
	}
	for _, tt := range tests {
		got, ok := patchBump(tt.in)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("patchBump(%q) = %q,%v want %q", tt.in, got, ok, tt.want)
		}
	}
}
