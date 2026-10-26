package resolve

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func appsFake(t *testing.T) *tooltest.FakeASC {
	t.Helper()
	f := tooltest.NewFakeASC(t)
	f.Handle(http.MethodGet, "/v1/apps", 200, body(t,
		app("100", "Hjul", "se.jvcr.hjul", "HJUL1"),
		app("200", "Hjul Pro", "se.jvcr.hjulpro", "HJUL2"),
		app("300", "Planera", "se.jvcr.planera", "777"),
		app("400", "Sikt", "se.jvcr.sikt", "SIKT"),
		app("500", "Sikt Lite", "se.jvcr.siktlite", "SIKTL"),
	))
	f.Handle(http.MethodGet, "/v1/apps?filter[bundleId]=se.jvcr.planera", 200, body(t, app("300", "Planera", "se.jvcr.planera", "777")))
	f.Handle(http.MethodGet, "/v1/apps?filter[bundleId]=se.unknown.x", 200, body(t))
	f.Handle(http.MethodGet, "/v1/apps/100", 200, single(t, app("100", "Hjul", "se.jvcr.hjul", "HJUL1")))
	f.Handle(http.MethodGet, "/v1/apps/777", 404, `{"errors":[{"status":"404","code":"NOT_FOUND"}]}`)
	serveVersions(t, f, map[string][]res{
		"100": {ver("v1", "MAC_OS", "1.0", "READY_FOR_DISTRIBUTION", ""), ver("v2", "IOS", "1.0", "READY_FOR_DISTRIBUTION", "")},
		"300": {ver("v3", "IOS", "2.0", "READY_FOR_DISTRIBUTION", "")},
	})
	return f
}

func TestAppRefForms(t *testing.T) {
	tests := []struct {
		ref        tool.AppRef
		defaultApp string
		wantID     string
		kind       string
		next       string
		candidates int
	}{
		{ref: "100", wantID: "100"},
		{ref: "se.jvcr.planera", wantID: "300"},
		{ref: "hjul", wantID: "100"}, // exact name beats the "Hjul Pro" prefix
		{ref: "hjul2", wantID: "200"},
		{ref: "777", wantID: "300"}, // numeric SKU after the id 404
		{ref: "plan", wantID: "300"},
		{ref: "LITE", wantID: "500"},
		{ref: "Sikt", wantID: "400"},
		{ref: "si", kind: "ambiguous", next: "apps_list", candidates: 2},
		{ref: "nope", kind: "notfound", next: "apps_list"},
		{ref: "se.unknown.x", kind: "notfound", next: "apps_list"},
		{ref: "", defaultApp: "Planera", wantID: "300"},
		{ref: "", kind: "ambiguous", next: "apps_list", candidates: 5},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q/%s", tt.ref, tt.defaultApp), func(t *testing.T) {
			r := New(appsFake(t), Options{DefaultApp: tt.defaultApp, Clock: tooltest.NewFakeClock()})
			got, err := r.App(context.Background(), tt.ref)
			next, n := wantErr(t, err, tt.kind)
			if next != tt.next || n != tt.candidates {
				t.Fatalf("next=%q candidates=%d, want %q %d", next, n, tt.next, tt.candidates)
			}
			if got.ID != tt.wantID {
				t.Fatalf("id = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestAppPlatformsOrderedAndOnlyApp(t *testing.T) {
	f := tooltest.NewFakeASC(t)
	f.Handle(http.MethodGet, "/v1/apps", 200, body(t, app("100", "Hjul", "se.jvcr.hjul", "")))
	serveVersions(t, f, map[string][]res{"100": {
		ver("v1", "MAC_OS", "1.0", "READY_FOR_DISTRIBUTION", ""),
		ver("v2", "IOS", "1.0", "READY_FOR_DISTRIBUTION", ""),
		ver("v3", "IOS", "1.1", "PREPARE_FOR_SUBMISSION", ""),
	}})
	got, err := New(f, Options{}).App(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []tool.Platform{tool.IOS, tool.MacOS}; got.ID != "100" || !slices.Equal(got.Platforms, want) {
		t.Fatalf("got %+v, want id 100 platforms %v", got, want)
	}
}

func TestAppNoAppsAndCandidateCap(t *testing.T) {
	f := tooltest.NewFakeASC(t)
	f.Handle(http.MethodGet, "/v1/apps", 200, body(t))
	_, err := New(f, Options{}).App(context.Background(), "")
	wantErr(t, err, "notfound")

	f = tooltest.NewFakeASC(t)
	var games []res
	for i := range 7 {
		games = append(games, app(fmt.Sprint(i+1), fmt.Sprintf("Game %d", i+1), fmt.Sprintf("se.g%d", i), ""))
	}
	f.Handle(http.MethodGet, "/v1/apps", 200, body(t, games...))
	_, err = New(f, Options{}).App(context.Background(), "game")
	if _, n := wantErr(t, err, "ambiguous"); n != maxCandidates {
		t.Fatalf("candidates = %d, want %d", n, maxCandidates)
	}
}

func TestAppCacheExpires(t *testing.T) {
	f := appsFake(t)
	clock := tooltest.NewFakeClock()
	r := New(f, Options{Clock: clock})
	ctx := context.Background()
	steps := []struct {
		advance        time.Duration
		ref            tool.AppRef
		lists, versReq int
	}{
		{0, "hjul", 1, 1},
		{time.Minute, "plan", 1, 2},          // list cached; new app's platforms fetched
		{time.Minute, "hjul", 1, 2},          // both cached
		{4 * time.Minute, "hjul", 2, 3},      // 6 min since fill: both refetched
		{30 * time.Second, "Hjul Pro", 2, 4}, // fresh list; app 200 platforms fetched once
		{30 * time.Second, "Hjul Pro", 2, 4}, // cached
		{0, "100", 2, 4},                     // id lookup, platforms still cached
	}
	for i, s := range steps {
		clock.Set(clock.Now().Add(s.advance))
		if _, err := r.App(ctx, s.ref); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		lists := len(f.RequestsTo(http.MethodGet, "/v1/apps")) - countQuery(f, "filter[bundleId]")
		vers := len(f.RequestsTo(http.MethodGet, "/v1/apps/{id}/appStoreVersions"))
		if lists != s.lists || vers != s.versReq {
			t.Fatalf("step %d: lists=%d versions=%d, want %d %d", i, lists, vers, s.lists, s.versReq)
		}
	}
}

func countQuery(f *tooltest.FakeASC, key string) int {
	n := 0
	for _, r := range f.RequestsTo(http.MethodGet, "/v1/apps") {
		if r.Query.Has(key) {
			n++
		}
	}
	return n
}
