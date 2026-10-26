package reviews

import (
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// R7 AO1: since presets resolve to a Pacific-day window that is echoed.
func TestListSincePresets(t *testing.T) {
	const d19 = "2026-10-19T10:00:00-07:00"
	for _, c := range []struct {
		since, want, from, until string
	}{
		{"today", "a1/r4", "2026-10-20T00:00:00-07:00", ""},
		{"yesterday", "a1/r3", "2026-10-19T00:00:00-07:00", "2026-10-20T00:00:00-07:00"},
		{"this_week", "a1/r4,a1/r3", "2026-10-19T00:00:00-07:00", ""},
		{"last_7_days", "a1/r4,a1/r3,a1/r2", "2026-10-13T00:00:00-07:00", ""},
	} {
		t.Run(c.since, func(t *testing.T) {
			k := listKit(t)
			k.Clock.Set(time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)) // Tue 13:00 Pacific
			k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200,
				page("", nil, review("r4", 5, d20, "USA", ""), review("r3", 4, d19, "USA", ""), review("r2", 3, d14, "USA", "")))
			res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"since":"`+c.since+`"}`))
			if got := strings.Join(ids(tooltest.Decode[listOut](t, res)), ","); got != c.want {
				t.Errorf("reviews = %s, want %s", got, c.want)
			}
			w := tooltest.Decode[struct {
				Window map[string]string `json:"window"`
			}](t, res).Window
			if w["preset"] != c.since || w["since"] != c.from || w["until"] != c.until {
				t.Errorf("window = %v", w)
			}
		})
	}
}

// More than limit reviews newer than the window are skipped while paging:
// they never count toward limit, and the window's reviews still come back.
func TestListSinceWindowPagesPastNewerReviews(t *testing.T) {
	k := listKit(t)
	k.Clock.Set(time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)) // Tue 13:00 Pacific
	next := "https://api.appstoreconnect.apple.com/v1/apps/a1/customerReviews?cursor=P2"
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200, page(next, nil,
		review("t3", 5, "2026-10-20T12:00:00-07:00", "USA", ""), review("t2", 5, "2026-10-20T11:00:00-07:00", "USA", ""),
		review("t1", 5, "2026-10-20T10:00:00-07:00", "USA", "")))
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews?cursor=P2", 200, page("", nil,
		review("y2", 4, "2026-10-19T15:00:00-07:00", "USA", ""), review("y1", 3, "2026-10-19T09:00:00-07:00", "USA", ""),
		review("old", 1, d14, "USA", "")))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"since":"yesterday","limit":2}`))
	o := tooltest.Decode[listOut](t, res)
	if got := strings.Join(ids(o), ","); got != "a1/y2,a1/y1" || o.Returned != 2 {
		t.Fatalf("reviews = %s returned = %d", got, o.Returned)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/{id}/customerReviews")); n != 2 {
		t.Errorf("pages = %d, want 2", n)
	}
}

// R7 AO7: app:"all" always reports apps_scanned (apps from /v1/apps) and
// apps_with_reviews, counted before the overall limit trims the list.
func TestListAllCountsAppsConsistently(t *testing.T) {
	k := listKit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[{"type":"apps","id":"a1","attributes":{"name":"Alpha"}}]}`)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200, page("", nil, review("r4", 5, d20, "USA", "")))
	o := tooltest.Decode[listOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"app":"all"}`)))
	if o.AppsScanned != 1 || o.AppsWithReviews != 1 {
		t.Fatalf("single-app all: scanned=%d with=%d", o.AppsScanned, o.AppsWithReviews)
	}

	k = listKit(t)
	k.ASC.Handle("GET", "/v1/apps", 200,
		`{"data":[{"type":"apps","id":"a1","attributes":{"name":"Alpha"}},{"type":"apps","id":"a2","attributes":{"name":"Beta"}}]}`)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200, page("", nil, review("r4", 5, d20, "USA", "")))
	k.ASC.Handle("GET", "/v1/apps/a2/customerReviews", 200, page("", nil, review("b1", 1, d14, "SWE", "")))
	o = tooltest.Decode[listOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"app":"all","limit":1}`)))
	if o.AppsScanned != 2 || o.AppsWithReviews != 2 || o.Returned != 1 {
		t.Fatalf("scanned=%d with=%d returned=%d", o.AppsScanned, o.AppsWithReviews, o.Returned)
	}
}
