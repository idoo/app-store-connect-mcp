//go:build dev

package main

import (
	"net/http"
	"testing"

	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
)

func TestStepsMatchSpec(t *testing.T) {
	idx, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	v := ids{app: "1", version: "2", appInfo: "3", versionLoc: "4"}
	for _, st := range steps() {
		if err := idx.Validate(tool.Request{Method: http.MethodGet, Path: st.path(v), Query: st.queryFor(v)}); err != nil {
			t.Errorf("%s: %v", st.name, err)
		}
	}
}

func TestGetOnlyGuard(t *testing.T) {
	g := getOnly{next: http.DefaultTransport}
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		req, err := http.NewRequest(m, "https://api.appstoreconnect.apple.com/v1/apps", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.RoundTrip(req); err == nil {
			t.Errorf("%s was not refused", m)
		}
	}
}
