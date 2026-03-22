package core

import (
	"slices"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

const appsPage = `{
 "data": [
  {"type":"apps","id":"100","attributes":{"name":"Hjul","bundleId":"com.idoo.hjul","sku":"HJUL","primaryLocale":"en-US"},
   "relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v1"}]}}},
  {"type":"apps","id":"200","attributes":{"name":"Sikt","bundleId":"se.jvcr.sift","sku":"SIFT","primaryLocale":"sv"},
   "relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v2"},{"type":"appStoreVersions","id":"v3"}]}}},
  {"type":"apps","id":"300","attributes":{"name":"Planera","bundleId":"com.idoo.planera","sku":"PLAN","primaryLocale":"en-US"},
   "relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v4"}]}}}
 ],
 "included": [
  {"type":"appStoreVersions","id":"v1","attributes":{"platform":"IOS"}},
  {"type":"appStoreVersions","id":"v2","attributes":{"platform":"MAC_OS"}},
  {"type":"appStoreVersions","id":"v3","attributes":{"platform":"IOS"}},
  {"type":"appStoreVersions","id":"v4","attributes":{"platform":"IOS"}}
 ],
 "links": {"self":"https://api.appstoreconnect.apple.com/v1/apps"}
}`

func TestAppsListFuzzy(t *testing.T) {
	tests := []struct {
		args string
		want []string
	}{
		{`{}`, []string{"Hjul", "Planera", "Sikt"}},
		{`{"query":"hjl"}`, []string{"Hjul"}},
		{`{"query":"SIFT"}`, []string{"Sikt"}},
		{`{"query":"idoo"}`, []string{"Hjul", "Planera"}},
		{`{"query":"plan"}`, []string{"Planera"}},
		{`{"query":"idoo hjul"}`, []string{"Hjul"}},
		{`{"query":"zzz"}`, []string{}},
		{`{"platform":"MAC_OS"}`, []string{"Sikt"}},
		{`{"limit":1}`, []string{"Hjul"}},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			k := kit(t)
			k.ASC.Handle("GET", "/v1/apps", 200, appsPage)
			res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_list"), k.Env, tt.args))
			out := tooltest.Decode[struct {
				Apps []appSummary `json:"apps"`
			}](t, res)
			names := []string{}
			for _, a := range out.Apps {
				names = append(names, a.Name)
			}
			if !slices.Equal(names, tt.want) {
				t.Errorf("names = %v, want %v", names, tt.want)
			}
			reqs := k.ASC.Requests()
			if len(reqs) != 1 || reqs[0].Query.Get("include") != "appStoreVersions" {
				t.Errorf("requests = %+v", reqs)
			}
		})
	}
}

func TestAppsListPlatforms(t *testing.T) {
	k := kit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, appsPage)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_list"), k.Env, `{"query":"Sikt"}`))
	tooltest.AssertGolden(t, "testdata/golden/apps_list_sikt.json", res)
}

const appDoc = `{
 "data": {"type":"apps","id":"100","attributes":{"name":"Hjul","bundleId":"com.idoo.hjul","sku":"HJUL","primaryLocale":"en-US"},
  "relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v5"},{"type":"appStoreVersions","id":"v4"},{"type":"appStoreVersions","id":"v3"},{"type":"appStoreVersions","id":"v2"},{"type":"appStoreVersions","id":"m1"}]}}},
 "included": [
  {"type":"appStoreVersions","id":"v5","attributes":{"platform":"IOS","versionString":"3.0","appVersionState":"WAITING_FOR_REVIEW","appStoreState":"WAITING_FOR_REVIEW","releaseType":"MANUAL","createdDate":"2026-10-25T00:00:00-07:00"}},
  {"type":"appStoreVersions","id":"v4","attributes":{"platform":"IOS","versionString":"2.1","appVersionState":"READY_FOR_DISTRIBUTION","appStoreState":"READY_FOR_SALE","releaseType":"MANUAL","createdDate":"2026-10-10T00:00:00-07:00"}},
  {"type":"appStoreVersions","id":"v3","attributes":{"platform":"IOS","versionString":"2.0","appVersionState":"READY_FOR_DISTRIBUTION","appStoreState":"READY_FOR_SALE","createdDate":"2026-09-01T00:00:00-07:00"}},
  {"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.0","appVersionState":"REPLACED_WITH_NEW_VERSION","createdDate":"2026-01-01T00:00:00-07:00"}},
  {"type":"appStoreVersions","id":"m1","attributes":{"platform":"MAC_OS","versionString":"1.0","appVersionState":"PREPARE_FOR_SUBMISSION","createdDate":"2026-10-20T00:00:00-07:00"}}
 ],
 "links": {"self":"https://api.appstoreconnect.apple.com/v1/apps/100"}
}`

const buildsDoc = `{
 "data": [
  {"type":"builds","id":"b2","attributes":{"version":"42","uploadedDate":"2026-10-24T10:00:00-07:00","processingState":"PROCESSING","expired":false,"minOsVersion":"18.0"},
   "relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p3"}}}},
  {"type":"builds","id":"b1","attributes":{"version":"41","uploadedDate":"2026-10-20T10:00:00-07:00","processingState":"VALID","expired":false,"minOsVersion":"18.0"},
   "relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p3"}}}}
 ],
 "included": [{"type":"preReleaseVersions","id":"p3","attributes":{"version":"3.0","platform":"IOS"}}],
 "links": {"self":"https://api.appstoreconnect.apple.com/v1/builds"}
}`

const appInfosDoc = `{
 "data": [{"type":"appInfos","id":"i1","attributes":{},
  "relationships":{"appInfoLocalizations":{"data":[{"type":"appInfoLocalizations","id":"l1"},{"type":"appInfoLocalizations","id":"l2"}]}}}],
 "included": [
  {"type":"appInfoLocalizations","id":"l1","attributes":{"locale":"sv"}},
  {"type":"appInfoLocalizations","id":"l2","attributes":{"locale":"en-US"}}
 ],
 "links": {"self":"https://api.appstoreconnect.apple.com/v1/apps/100/appInfos"}
}`

func TestAppsGet(t *testing.T) {
	k := kit(t)
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "com.idoo.hjul", SKU: "HJUL"}}
	k.ASC.Handle("GET", "/v1/apps/{id}", 200, appDoc)
	k.ASC.Handle("GET", "/v1/builds", 200, buildsDoc)
	k.ASC.Handle("GET", "/v1/apps/{id}/appInfos", 200, appInfosDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_get"), k.Env, `{"app":"Hjul"}`))

	if n := len(k.ASC.Requests()); n > 4 {
		t.Errorf("apps_get made %d requests, want at most 4", n)
	}
	out := tooltest.Decode[appOverview](t, res)
	if len(out.Live) != 1 || out.Live[0].Version != "2.1" {
		t.Errorf("live = %+v, want only 2.1", out.Live)
	}
	var inFlight []string
	for _, v := range out.InFlight {
		inFlight = append(inFlight, v.Platform+" "+v.Version+" "+v.State)
	}
	if want := []string{"IOS 3.0 WAITING_FOR_REVIEW", "MAC_OS 1.0 PREPARE_FOR_SUBMISSION"}; !slices.Equal(inFlight, want) {
		t.Errorf("in flight = %v, want %v", inFlight, want)
	}
	if len(out.Builds) != 2 || out.Builds[0].Number != "42" || out.Builds[0].Version != "3.0" {
		t.Errorf("builds = %+v", out.Builds)
	}
	if !slices.Equal(out.Locales, []string{"en-US", "sv"}) || !slices.Equal(out.Platforms, []string{"IOS", "MAC_OS"}) {
		t.Errorf("locales = %v platforms = %v", out.Locales, out.Platforms)
	}
	b := k.ASC.RequestsTo("GET", "/v1/builds")
	if len(b) != 1 || b[0].Query.Get("filter[app]") != "100" || b[0].Query.Get("sort") != "-uploadedDate" || b[0].Query.Get("limit") != "5" {
		t.Errorf("builds request = %+v", b)
	}
	tooltest.AssertGolden(t, "testdata/golden/apps_get.json", res)
	type logged struct {
		Method, Path string
		Query        map[string][]string
	}
	var log []logged
	for _, r := range k.ASC.Requests() {
		log = append(log, logged{r.Method, r.Path, r.Query})
	}
	tooltest.AssertGolden(t, "testdata/golden/apps_get_requests.json", log)
}

func TestAppsGetUnknownApp(t *testing.T) {
	k := kit(t)
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "com.idoo.hjul"}}
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "apps_get"), k.Env, `{"app":"Nope"}`), "not_found")
	if len(body.Next) == 0 || body.Next[0].Tool != "apps_list" {
		t.Errorf("next = %+v", body.Next)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Error("ASC called for an unresolved app")
	}
}
