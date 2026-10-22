package release

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// fixture describes one synthetic app for release_status tests.
type fixture struct {
	locales        []string
	first          bool // no prior released version
	noBuild        bool
	encryptionNull bool
	keywords       string // keywords override for the last locale
	noIPhoneShots  bool   // primary locale lacks the required iPhone slot
	noPrice        bool
	// state overrides v1's appVersionState; a locked state also opens a
	// WAITING_FOR_REVIEW submission.
	state string
	// korea answers the GRAC question with nil and makes KOR available (true)
	// or unavailable (false); nil leaves the declaration complete.
	korea *bool
	// buildVersion is the attached build's marketing version (default 1.4).
	buildVersion string
	noIPad       bool // no iPad screenshot sets anywhere
	// ageUnanswered leaves a general (non-Korea) age rating question nil.
	ageUnanswered bool
	// iapState overrides the Pro IAP: product state, no versions.
	iapState string
	// subtitle overrides the appInfo subtitle of the last locale.
	subtitle string
	// macOS adds an editable MAC_OS 3.6 version next to the iOS one.
	macOS bool
	// a11y is "" (IPHONE and IPAD published), "none" or "draft" (IPHONE
	// draft only).
	a11y string
	// age overrides age rating declaration attributes.
	age map[string]any
	// cpp routes the custom product page GETs (nil: no pages).
	cpp func(k *tooltest.Kit)
	// library routes the Asset Library placement GETs (nil: none placed).
	library func(k *tooltest.Kit)
	// placed are the localization ids (loc0…) reported as having library
	// placements by the discovery read (default: loc0 when library is set).
	placed []string
	// attachments are App Review attachment file names.
	attachments []string
	// devices are the deviceModel values App Store thinning lists for the
	// attached build's App bundle (nil: none, i.e. unknown).
	devices []string
	// extraDisplays adds one COMPLETE screenshot set per display type to
	// every localization.
	extraDisplays []string
}

func readyFixture() fixture { return fixture{locales: []string{"en-US", "de-DE"}} }

type jres struct {
	Type, ID string
	Attrs    map[string]any
	Rels     map[string]any // name → id or []id with types encoded "type:id"
}

func (r jres) raw() map[string]any {
	m := map[string]any{"type": r.Type, "id": r.ID, "attributes": r.Attrs}
	if len(r.Rels) > 0 {
		rels := map[string]any{}
		for name, v := range r.Rels {
			switch v := v.(type) {
			case string:
				t, id, _ := strings.Cut(v, ":")
				rels[name] = map[string]any{"data": map[string]any{"type": t, "id": id}}
			case []string:
				data := []any{}
				for _, x := range v {
					t, id, _ := strings.Cut(x, ":")
					data = append(data, map[string]any{"type": t, "id": id})
				}
				rels[name] = map[string]any{"data": data}
			}
		}
		m["relationships"] = rels
	}
	return m
}

func doc(t *testing.T, data any, included ...jres) string {
	t.Helper()
	d := map[string]any{}
	switch v := data.(type) {
	case jres:
		d["data"] = v.raw()
	case []jres:
		arr := []any{}
		for _, r := range v {
			arr = append(arr, r.raw())
		}
		d["data"] = arr
	}
	if len(included) > 0 {
		inc := []any{}
		for _, r := range included {
			inc = append(inc, r.raw())
		}
		d["included"] = inc
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const notFound = `{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`

func setup(t *testing.T, f fixture) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t).ReadOnly()
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "se.jvcr.hjul", Platforms: []tool.Platform{tool.IOS}}}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{
		"editable": {ID: "v1", String: "1.4", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"},
		"1.4":      {ID: "v1", String: "1.4", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"},
	}
	k.Resolve.Builds = map[tool.BuildRef]tool.Build{
		"latest":     {ID: "b2", Number: "43", ProcessingState: "VALID"},
		"latest:1.4": {ID: "b2", Number: "43", ProcessingState: "VALID"},
	}
	bv := "1.4"
	if f.buildVersion != "" {
		bv = f.buildVersion
	}
	k.ASC.Handle("GET", "/v1/builds/{id}/preReleaseVersion", 200, doc(t, jres{Type: "preReleaseVersions", ID: "pre1", Attrs: map[string]any{"version": bv}}))
	k.ASC.Handle("GET", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b1", Rels: map[string]any{"buildBundles": []string{"buildBundles:bb1"}}},
		jres{Type: "buildBundles", ID: "bb1", Attrs: map[string]any{"bundleType": "APP"}}))
	var sizes []jres
	for i, m := range f.devices {
		sizes = append(sizes, jres{Type: "buildBundleFileSizes", ID: fmt.Sprintf("fs%d", i), Attrs: map[string]any{"deviceModel": m}})
	}
	k.ASC.Handle("GET", "/v1/buildBundles/{id}/buildBundleFileSizes", 200, doc(t, sizes))

	prior := []jres{}
	if !f.first {
		prior = append(prior, jres{Type: "appStoreVersions", ID: "v0", Attrs: map[string]any{"platform": "IOS", "versionString": "1.3", "appVersionState": "READY_FOR_DISTRIBUTION", "releaseType": "AFTER_APPROVAL"}})
	}
	verIDs := []string{"appStoreVersions:v1"}
	for _, p := range prior {
		verIDs = append(verIDs, "appStoreVersions:"+p.ID)
	}
	state := "PREPARE_FOR_SUBMISSION"
	if f.state != "" {
		state = f.state
	}
	appInc := append([]jres{{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"platform": "IOS", "versionString": "1.4", "appVersionState": state, "releaseType": "AFTER_APPROVAL"}}}, prior...)
	k.ASC.Handle("GET", "/v1/apps/{id}", 200, doc(t, jres{
		Type: "apps", ID: "100",
		Attrs: map[string]any{"primaryLocale": "en-US", "contentRightsDeclaration": "DOES_NOT_USE_THIRD_PARTY_CONTENT"},
		Rels:  map[string]any{"appStoreVersions": verIDs},
	}, appInc...))

	// Active versions on every platform (release_status without a platform).
	active := []jres{{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"platform": "IOS", "versionString": "1.4", "appVersionState": state}}}
	if f.macOS {
		active = append(active, jres{Type: "appStoreVersions", ID: "m1", Attrs: map[string]any{"platform": "MAC_OS", "versionString": "3.6", "appVersionState": "PREPARE_FOR_SUBMISSION"}})
	}
	k.ASC.Handle("GET", "/v1/apps/{id}/appStoreVersions", 200, doc(t, active))

	// Version with localizations, build, review detail.
	var locRels, infoLocRels []string
	var inc, infoInc []jres
	for i, loc := range f.locales {
		kw := "boat,sail,wind"
		if i == len(f.locales)-1 && f.keywords != "" {
			kw = f.keywords
		}
		wn := "Bug fixes."
		if f.first {
			wn = ""
		}
		id := fmt.Sprintf("loc%d", i)
		locRels = append(locRels, "appStoreVersionLocalizations:"+id)
		inc = append(inc, jres{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{
			"locale": loc, "description": "A sailing app.", "keywords": kw, "supportUrl": "https://jvcr.se/support", "whatsNew": wn, "promotionalText": "Sail on.",
		}})
		iid := fmt.Sprintf("info%d", i)
		infoLocRels = append(infoLocRels, "appInfoLocalizations:"+iid)
		sub := "Sail better"
		if i == len(f.locales)-1 && f.subtitle != "" {
			sub = f.subtitle
		}
		infoInc = append(infoInc, jres{Type: "appInfoLocalizations", ID: iid, Attrs: map[string]any{"locale": loc, "name": "Hjul", "subtitle": sub, "privacyPolicyUrl": "https://jvcr.se/privacy"}})
	}
	verRels := map[string]any{"appStoreVersionLocalizations": locRels, "appStoreReviewDetail": "appStoreReviewDetails:rd1"}
	inc = append(inc, jres{Type: "appStoreReviewDetails", ID: "rd1", Attrs: map[string]any{
		"contactFirstName": "Ivan", "contactLastName": "D", "contactPhone": "+46700000000", "contactEmail": "a@b.se", "demoAccountRequired": false,
	}})
	if !f.noBuild {
		enc := any(false)
		if f.encryptionNull {
			enc = nil
		}
		verRels["build"] = "builds:b1"
		inc = append(inc, jres{Type: "builds", ID: "b1", Attrs: map[string]any{"version": "42", "processingState": "VALID", "expired": false, "usesNonExemptEncryption": enc}})
	}
	k.ASC.Handle("GET", "/v1/appStoreVersions/{id}", 200, doc(t, jres{
		Type: "appStoreVersions", ID: "v1",
		Attrs: map[string]any{"versionString": "1.4", "platform": "IOS", "appVersionState": state, "releaseType": "AFTER_APPROVAL"},
		Rels:  verRels,
	}, inc...))

	age := map[string]any{"violenceCartoonOrFantasy": "NONE", "kidsAgeBand": nil}
	if f.ageUnanswered {
		age["messagingAndChat"] = nil
	}
	maps.Copy(age, f.age)
	if f.korea != nil {
		age["gracRatingClassificationNumber"] = nil
		k.ASC.Handle("GET", "/v2/appAvailabilities/{id}/territoryAvailabilities", 200, doc(t, []jres{
			{Type: "territoryAvailabilities", ID: "ta1", Attrs: map[string]any{"available": true}, Rels: map[string]any{"territory": "territories:USA"}},
			{Type: "territoryAvailabilities", ID: "ta2", Attrs: map[string]any{"available": *f.korea}, Rels: map[string]any{"territory": "territories:KOR"}},
		}))
	}
	infoInc = append(infoInc, jres{Type: "ageRatingDeclarations", ID: "age1", Attrs: age})
	k.ASC.Handle("GET", "/v1/apps/{id}/appInfos", 200, doc(t, []jres{{
		Type: "appInfos", ID: "ai1", Attrs: map[string]any{"state": "PREPARE_FOR_SUBMISSION"},
		Rels: map[string]any{"appInfoLocalizations": infoLocRels, "ageRatingDeclaration": "ageRatingDeclarations:age1"},
	}}, infoInc...))

	k.ASC.HandleFunc("GET", "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets", func(req tool.Request) tooltest.Fixture {
		id := strings.Split(req.Path, "/")[3]
		sets := []jres{}
		var shots []jres
		if !f.noIPhoneShots || id != "loc0" {
			sets = append(sets, jres{Type: "appScreenshotSets", ID: "set-" + id, Attrs: map[string]any{"screenshotDisplayType": "APP_IPHONE_67"}, Rels: map[string]any{"appScreenshots": []string{"appScreenshots:s-" + id}}})
			shots = append(shots, jres{Type: "appScreenshots", ID: "s-" + id, Attrs: map[string]any{"assetDeliveryState": map[string]any{"state": "COMPLETE"}}})
		}
		if !f.noIPad {
			sets = append(sets, jres{Type: "appScreenshotSets", ID: "ipad-" + id, Attrs: map[string]any{"screenshotDisplayType": "APP_IPAD_PRO_3GEN_129"}, Rels: map[string]any{"appScreenshots": []string{"appScreenshots:p-" + id}}})
			shots = append(shots, jres{Type: "appScreenshots", ID: "p-" + id, Attrs: map[string]any{"assetDeliveryState": map[string]any{"state": "COMPLETE"}}})
		}
		for _, d := range f.extraDisplays {
			sets = append(sets, jres{Type: "appScreenshotSets", ID: d + "-" + id, Attrs: map[string]any{"screenshotDisplayType": d}, Rels: map[string]any{"appScreenshots": []string{"appScreenshots:" + d + "-s-" + id}}})
			shots = append(shots, jres{Type: "appScreenshots", ID: d + "-s-" + id, Attrs: map[string]any{"assetDeliveryState": map[string]any{"state": "COMPLETE"}}})
		}
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, sets, shots...))}
	})
	if f.library != nil {
		f.library(k)
		if f.placed == nil {
			f.placed = []string{"loc0"}
		}
	} else {
		k.ASC.Handle("GET", "/v1/appStoreVersionLocalizations/{id}/placements", 200, `{"data":[]}`)
	}
	k.ASC.HandleFunc("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations?include=placements", func(tool.Request) tooltest.Fixture {
		var locs []jres
		for i, loc := range f.locales {
			id := fmt.Sprintf("loc%d", i)
			ps := []string{}
			if slices.Contains(f.placed, id) {
				ps = append(ps, "appAssetLibraryPlacements:p-"+id)
			}
			locs = append(locs, jres{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{"locale": loc}, Rels: map[string]any{"placements": ps}})
		}
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, locs))}
	})
	atts := []jres{}
	for i, n := range f.attachments {
		atts = append(atts, jres{Type: "appStoreReviewAttachments", ID: fmt.Sprintf("att%d", i), Attrs: map[string]any{"fileName": n}})
	}
	k.ASC.Handle("GET", "/v1/appStoreReviewDetails/{id}/appStoreReviewAttachments", 200, doc(t, atts))
	if f.noPrice {
		k.ASC.Handle("GET", "/v1/apps/{id}/appPriceSchedule", 404, notFound)
	} else {
		k.ASC.Handle("GET", "/v1/apps/{id}/appPriceSchedule", 200, doc(t, jres{Type: "appPriceSchedules", ID: "100", Attrs: map[string]any{}, Rels: map[string]any{"baseTerritory": "territories:USA"}}))
	}
	k.ASC.Handle("GET", "/v1/apps/{id}/appAvailabilityV2", 200, doc(t, jres{Type: "appAvailabilities", ID: "100", Attrs: map[string]any{"availableInNewTerritories": true}}))
	if slices.Contains(lockedStates, state) {
		k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, doc(t, []jres{{Type: "reviewSubmissions", ID: "sub1", Attrs: map[string]any{"state": "WAITING_FOR_REVIEW"}}}))
	} else {
		k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, `{"data":[]}`)
	}
	if f.iapState != "" {
		k.ASC.Handle("GET", "/v1/apps/{id}/inAppPurchasesV2", 200, doc(t, []jres{{Type: "inAppPurchases", ID: "iap1", Attrs: map[string]any{"name": "Pro", "productId": "pro", "state": f.iapState}}}))
	} else {
		k.ASC.Handle("GET", "/v1/apps/{id}/inAppPurchasesV2", 200, doc(t, []jres{{Type: "inAppPurchases", ID: "iap1", Attrs: map[string]any{"name": "Pro", "state": "READY_TO_SUBMIT"}, Rels: map[string]any{"versions": []string{"inAppPurchaseVersions:iv1"}}}},
			jres{Type: "inAppPurchaseVersions", ID: "iv1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}))
	}
	k.ASC.Handle("GET", "/v1/apps/{id}/subscriptionGroups", 200, `{"data":[]}`)
	decls := []jres{
		{Type: "accessibilityDeclarations", ID: "ad1", Attrs: map[string]any{"deviceFamily": "IPHONE", "state": "PUBLISHED"}},
		{Type: "accessibilityDeclarations", ID: "ad2", Attrs: map[string]any{"deviceFamily": "IPAD", "state": "PUBLISHED"}},
	}
	switch f.a11y {
	case "none":
		decls = nil
	case "draft":
		decls = []jres{{Type: "accessibilityDeclarations", ID: "ad1", Attrs: map[string]any{"deviceFamily": "IPHONE", "state": "DRAFT"}}}
	}
	if decls == nil {
		k.ASC.Handle("GET", "/v1/apps/{id}/accessibilityDeclarations", 200, `{"data":[]}`)
	} else {
		k.ASC.Handle("GET", "/v1/apps/{id}/accessibilityDeclarations", 200, doc(t, decls))
	}
	if f.cpp != nil {
		f.cpp(k)
	} else {
		k.ASC.Handle("GET", "/v1/apps/{id}/appCustomProductPages", 200, `{"data":[]}`)
	}
	return k
}

func call(t *testing.T, k *tooltest.Kit, args string) (statusOut, *tool.Result) {
	t.Helper()
	tl, ok := tool.Lookup("release_status")
	if !ok {
		t.Fatal("release_status not registered")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args))
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("release_status sent %d mutations", n)
	}
	return tooltest.Decode[statusOut](t, res), res
}

func checkByID(t *testing.T, out statusOut, id string) check {
	t.Helper()
	for _, c := range out.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, out.Checks)
	return check{}
}

func ready(out statusOut) bool { return out.Summary.Ready != nil && *out.Summary.Ready }

func TestReleaseStatusReady(t *testing.T) {
	k := setup(t, readyFixture())
	out, res := call(t, k, `{"app":"Hjul"}`)
	for _, c := range out.Checks {
		want := statusOK
		switch c.ID {
		case "app_privacy":
			want = statusManual
		case "iphone_duo_screenshots": // pending slot, not uploadable via the API
			want = statusWarn
		}
		if c.Status != want {
			t.Errorf("check %s = %s: %s %v", c.ID, c.Status, c.Detail, c.Items)
		}
	}
	if !ready(out) || out.Summary.Block != 0 || out.Summary.Manual != 1 || out.Summary.Phase != phasePreparing || out.Plan != nil {
		t.Errorf("summary = %+v plan = %+v", out.Summary, out.Plan)
	}
	if out.Version.ID != "v1" || out.Version.AppVersionState != "PREPARE_FOR_SUBMISSION" || out.Build == nil || out.Build.ID != "b1" {
		t.Errorf("version/build = %+v %+v", out.Version, out.Build)
	}
	if len(k.Plans.Proposed) != 0 {
		t.Errorf("plans proposed without desired: %d", len(k.Plans.Proposed))
	}
	if got := checkByID(t, out, "in_app_products"); !slices.Contains(got.Items, "iap Pro") {
		t.Errorf("ready IAP not listed: %+v", got)
	}
	tooltest.AssertGolden(t, "testdata/status/ready.json", res)
}

func TestReleaseStatusBlocked(t *testing.T) {
	f := readyFixture()
	f.noIPhoneShots = true
	f.keywords = strings.Repeat("k", 101)
	f.noBuild = true
	f.noPrice = true
	k := setup(t, f)
	out, _ := call(t, k, `{"app":"Hjul"}`)
	want := map[string]string{
		"build": statusBlock, "export_compliance": statusBlock, "listing": statusBlock,
		"screenshots": statusBlock, "pricing_availability": statusBlock, "whats_new": statusOK,
	}
	for id, st := range want {
		if c := checkByID(t, out, id); c.Status != st {
			t.Errorf("%s = %s (%s %v), want %s", id, c.Status, c.Detail, c.Items, st)
		}
	}
	if c := checkByID(t, out, "screenshots"); len(c.Items) != 1 || !strings.Contains(c.Items[0], "en-US: no screenshots for APP_IPHONE_67 or APP_IPHONE_65") {
		t.Errorf("screenshot items = %v", c.Items)
	}
	if c := checkByID(t, out, "listing"); len(c.Items) != 1 || !strings.HasPrefix(c.Items[0], "de-DE: keywords is 101 characters") {
		t.Errorf("listing items = %v", c.Items)
	}
	if ready(out) || out.Summary.Block != 5 {
		t.Errorf("summary = %+v", out.Summary)
	}
}

func TestReleaseStatusExportComplianceUnanswered(t *testing.T) {
	f := readyFixture()
	f.encryptionNull = true
	out, _ := call(t, setup(t, f), `{}`)
	if c := checkByID(t, out, "export_compliance"); c.Status != statusBlock || c.FixVia.Tool != "release_prepare" {
		t.Errorf("export_compliance = %+v", c)
	}
}

func TestReleaseStatusFirstVersion(t *testing.T) {
	f := readyFixture()
	f.first = true
	out, _ := call(t, setup(t, f), `{"app":"100"}`)
	if c := checkByID(t, out, "whats_new"); c.Status != statusOK || !strings.Contains(c.Detail, "first version") {
		t.Errorf("whats_new = %+v", c)
	}
	if c := checkByID(t, out, "app_privacy"); c.Status != statusManual || c.FixVia.Manual != "https://appstoreconnect.apple.com/apps/100/distribution/privacy" {
		t.Errorf("app_privacy = %+v", c)
	}
}

func TestReleaseStatusRequestBudget(t *testing.T) {
	locs := []string{"en-US", "de-DE", "fr-FR", "es-ES", "it", "ja", "ko", "zh-Hans", "zh-Hant", "pt-BR", "nl-NL", "sv", "da", "no", "fi"}
	k := setup(t, fixture{locales: locs})
	out, _ := call(t, k, `{"app":"Hjul","version":"1.4"}`)
	if !ready(out) {
		t.Errorf("15-locale fixture not ready: %+v", out.Checks)
	}
	reqs := k.ASC.Requests()
	// 26 + the custom product page list + placed-localization discovery + review attachments
	// + the attached build's bundles + its App bundle's device models
	if len(reqs) > 31 {
		t.Fatalf("%d requests for 15 locales, want ≤ 31", len(reqs))
	}
	if got := k.ASC.RequestsTo("GET", "/v1/appStoreVersionLocalizations/{id}/placements"); len(got) != 0 {
		t.Errorf("placements read without placed localizations: %d", len(got))
	}

	// 2 of 15 localizations have library placements: one read each.
	k2 := setup(t, fixture{locales: locs, placed: []string{"loc0", "loc7"}})
	call(t, k2, `{"app":"Hjul","version":"1.4"}`)
	if n := len(k2.ASC.Requests()); n > 33 { // 27 + discovery + 2 placed + review attachments + build bundles + device models
		t.Fatalf("%d requests with 2 placed localizations, want ≤ 33", n)
	}
	if got := k2.ASC.RequestsTo("GET", "/v1/appStoreVersionLocalizations/{id}/placements"); len(got) != 2 {
		t.Errorf("placements reads = %d, want 2", len(got))
	}
	for _, r := range reqs {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET %s %s", r.Method, r.Path)
		}
	}
	if got := k.ASC.RequestsTo("GET", "/v1/appStoreVersions/{id}"); len(got) != 1 || got[0].Query.Get("include") != "appStoreVersionLocalizations,build,appStoreReviewDetail,appStoreVersionPhasedRelease" {
		t.Errorf("version request = %+v", got)
	}
}

// Without a platform, release_status names the app's other active platforms;
// an explicit platform costs no extra request.
func TestReleaseStatusOtherPlatforms(t *testing.T) {
	f := readyFixture()
	f.macOS = true
	k := setup(t, f)
	out, res := call(t, k, `{"app":"Hjul"}`)
	if !out.PlatformInferred || len(out.OtherPlatforms) != 1 || out.OtherPlatforms[0].Platform != "MAC_OS" || out.OtherPlatforms[0].Version != "3.6" {
		t.Fatalf("other platforms = %+v (inferred %v)", out.OtherPlatforms, out.PlatformInferred)
	}
	found := false
	for _, n := range res.Next {
		found = found || (n.Tool == "release_status" && n.Args["platform"] == "MAC_OS")
	}
	if !found {
		t.Fatalf("next = %+v", res.Next)
	}
	inferred := len(k.ASC.Requests())

	k2 := setup(t, f)
	out, _ = call(t, k2, `{"app":"Hjul","platform":"IOS"}`)
	if out.PlatformInferred || len(out.OtherPlatforms) != 0 || len(k2.ASC.RequestsTo("GET", "/v1/apps/{id}/appStoreVersions")) != 0 {
		t.Fatalf("explicit platform: %+v", out.OtherPlatforms)
	}
	if got := len(k2.ASC.Requests()); got != inferred-1 {
		t.Fatalf("explicit platform made %d requests, inferred %d", got, inferred)
	}
}

func TestReleaseStatusDesiredPlans(t *testing.T) {
	k := setup(t, readyFixture())
	out, res := call(t, k, `{"app":"Hjul","desired":{"build":"latest","whats_new":{"*":"New sails.","fr":"Voiles."},"release_type":"MANUAL","phased":true,"submit":true}}`)
	p := out.Plan
	if p == nil || p.PreparePlanID == "" || p.SubmitPlanID == "" {
		t.Fatalf("plan = %+v", p)
	}
	for _, want := range []string{"attach build 43 (b2)", "set What's New for de-DE", "set What's New for en-US", "set release type MANUAL", "enable phased release (starts INACTIVE, begins on release)"} {
		if !slices.Contains(p.Changes, want) {
			t.Errorf("changes %v lack %q", p.Changes, want)
		}
	}
	if !slices.Equal(p.SkippedLocales, []string{"fr-FR"}) {
		t.Errorf("skipped = %v", p.SkippedLocales)
	}
	if len(res.Next) != 2 || res.Next[0].Tool != "release_prepare" || res.Next[0].Args["plan_id"] != p.PreparePlanID || res.Next[1].Tool != "release_submit" {
		t.Errorf("next = %+v", res.Next)
	}

	// The prepare plan verifies with the shared key and a fresh fingerprint.
	ctx := t.Context()
	app := k.Resolve.Apps[0]
	yes := true
	d := releaseDesired{Build: "latest", WhatsNew: map[string]string{"*": "New sails.", "fr": "Voiles."}, ReleaseType: "MANUAL", Phased: &yes}
	key, err := prepareArgsKey(app, tool.IOS, "1.4", "b2", d)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := loadReleaseSnapshot(ctx, k.Env, app.ID, tool.IOS, "1.4")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := snap.State.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Plans.Verify(ctx, p.PreparePlanID, "release_prepare", key, fp); err != nil {
		t.Errorf("prepare plan does not verify: %v", err)
	}
	// The submit plan is bound to the post-prepare state: stale now.
	if err := k.Plans.Verify(ctx, p.SubmitPlanID, "release_submit", submitArgsKey(app, tool.IOS, "1.4"), fp); err == nil {
		t.Error("submit plan verified against the pre-prepare state")
	}
}

func TestReleaseStatusDesiredFirstVersionSkipsWhatsNew(t *testing.T) {
	f := readyFixture()
	f.first = true
	k := setup(t, f)
	out, _ := call(t, k, `{"desired":{"whats_new":{"en-US":"Hello"}}}`)
	if out.Plan == nil || out.Plan.PreparePlanID != "" || len(out.Plan.Changes) != 0 {
		t.Errorf("plan = %+v", out.Plan)
	}
	if len(k.Plans.Proposed) != 0 {
		t.Errorf("proposed %d plans for a no-op", len(k.Plans.Proposed))
	}
}

func TestReleaseStatusMissingVersion(t *testing.T) {
	k := setup(t, readyFixture())
	out, _ := call(t, k, `{"version":"2.0","desired":{"release_type":"AFTER_APPROVAL"}}`)
	if c := checkByID(t, out, "version"); c.Status != statusBlock || out.Version.Exists {
		t.Errorf("version check = %+v", c)
	}
	if out.Plan == nil || out.Plan.PreparePlanID == "" || out.Plan.Changes[0] != "create IOS version 2.0" {
		t.Errorf("plan = %+v", out.Plan)
	}
}

// lockedKit is the setup fixture with v1 (1.4) WAITING_FOR_REVIEW and an open
// submission; explicit keeps "1.4" resolvable, otherwise nothing resolves
// (no editable version). The build is missing to prove locked blocks soften.
func lockedKit(t *testing.T, explicit bool) *tooltest.Kit {
	t.Helper()
	f := readyFixture()
	f.state, f.noBuild = "WAITING_FOR_REVIEW", true
	k := setup(t, f)
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{}
	if explicit {
		k.Resolve.Versions["1.4"] = tool.Version{ID: "v1", String: "1.4", Platform: tool.IOS, State: "WAITING_FOR_REVIEW"}
	}
	return k
}

func assertNoCancelFix(t *testing.T, out statusOut, res *tool.Result) {
	t.Helper()
	for _, c := range out.Checks {
		if c.FixVia != nil && c.FixVia.Tool == "release_cancel" {
			t.Errorf("check %s suggests release_cancel", c.ID)
		}
		if c.Status == statusBlock {
			t.Errorf("check %s blocks while in review: %s", c.ID, c.Detail)
		}
	}
	for _, n := range res.Next {
		if n.Tool == "release_cancel" {
			t.Errorf("next suggests release_cancel: %+v", res.Next)
		}
	}
	if out.Summary.Ready != nil || out.Summary.Phase != phaseInReview {
		t.Errorf("summary = %+v", out.Summary)
	}
	if len(res.Next) == 0 || res.Next[0].Tool != "release_review_status" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestReleaseStatusNoEditableReportsInFlight(t *testing.T) {
	k := lockedKit(t, false)
	out, res := call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "version"); c.Status != statusInfo || !strings.Contains(c.Detail, "1.4 is WAITING_FOR_REVIEW") || c.FixVia != nil {
		t.Errorf("version = %+v", c)
	}
	f := out.InFlight
	if f == nil || f.ID != "v1" || f.AppVersionState != "WAITING_FOR_REVIEW" || len(f.Options) != 1 || f.Options[0].Tool != "release_cancel" || f.Options[0].Reason == "" {
		t.Fatalf("in_flight = %+v", f)
	}
	assertNoCancelFix(t, out, res)
}

func TestReleaseStatusExplicitVersionInReview(t *testing.T) {
	k := lockedKit(t, true)
	out, res := call(t, k, `{"app":"Hjul","version":"1.4"}`)
	if c := checkByID(t, out, "build"); c.Status != statusInfo || !strings.HasPrefix(c.Detail, "locked while WAITING_FOR_REVIEW") {
		t.Errorf("build = %+v", c)
	}
	if c := checkByID(t, out, "submission"); c.Status != statusInfo {
		t.Errorf("submission = %+v", c)
	}
	if out.InFlight != nil || out.Version.AppVersionState != "WAITING_FOR_REVIEW" {
		t.Errorf("version = %+v in_flight = %+v", out.Version, out.InFlight)
	}
	assertNoCancelFix(t, out, res)
}

func TestReleaseStatusNothingInFlightSuggestsPrepare(t *testing.T) {
	k := setup(t, readyFixture())
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{}
	out, res := call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "version"); c.Status != statusBlock || c.FixVia == nil || c.FixVia.Tool != "release_prepare" || out.InFlight != nil {
		t.Errorf("version = %+v in_flight = %+v", c, out.InFlight)
	}
	if out.Summary.Phase != phaseMissing || len(res.Next) == 0 || res.Next[0].Tool != "release_prepare" {
		t.Errorf("summary = %+v next = %+v", out.Summary, res.Next)
	}
}

func TestReleaseStatusKoreaAgeRating(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		want      string
	}{{"in Korea", true, statusWarn}, {"not in Korea", false, statusInfo}} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyFixture()
			f.korea = &tc.available
			k := setup(t, f)
			out, _ := call(t, k, `{"app":"Hjul"}`)
			if c := checkByID(t, out, "age_rating"); c.Status != tc.want || !slices.Equal(c.Items, []string{"gracRatingClassificationNumber"}) {
				t.Errorf("age_rating = %+v", c)
			}
		})
	}
}

// ReleaseManager#1: a build of another marketing version blocks, and
// build:"latest" resolves the newest build of the version, not of the app.
func TestReleaseStatusBuildVersionMismatch(t *testing.T) {
	f := readyFixture()
	f.buildVersion = "1.5"
	k := setup(t, f)
	k.Resolve.Builds["latest"] = tool.Build{ID: "b9", Number: "99", ProcessingState: "VALID"} // a 1.5 upload
	out, _ := call(t, k, `{"app":"Hjul","desired":{"build":"latest"}}`)
	if c := checkByID(t, out, "build"); c.Status != statusBlock || !strings.Contains(c.Detail, "version 1.5, not 1.4") {
		t.Errorf("build = %+v", c)
	}
	if out.Plan == nil || !slices.Contains(out.Plan.Changes, "attach build 43 (b2)") {
		t.Errorf("plan = %+v", out.Plan)
	}
}

// ReleaseManager#3: READY_TO_SUBMIT IAPs are ready, WAITING_FOR_UPLOAD and
// rejected ones are pending; none vanish.
func TestCheckProductsStates(t *testing.T) {
	classify := func(c check, label, st string) string {
		switch {
		case slices.Contains(c.Items, label):
			return "ready"
		case slices.Contains(c.Items, label+": "+st):
			return "pending"
		}
		return "none"
	}
	for st, want := range map[string]string{
		"READY_TO_SUBMIT": "ready", "READY_FOR_REVIEW": "ready", "WAITING_FOR_UPLOAD": "pending",
		"MISSING_METADATA": "pending", "REJECTED": "pending", "DEVELOPER_REJECTED": "pending",
		"DEVELOPER_ACTION_NEEDED": "pending", "PREPARE_FOR_SUBMISSION": "pending",
		"APPROVED": "none", "WAITING_FOR_REVIEW": "none", "IN_REVIEW": "none", "REMOVED_FROM_SALE": "none",
	} {
		if got := classify(checkProducts(&statusSnapshot{IAPs: map[string]string{"Pro": st}}), "iap Pro", st); got != want {
			t.Errorf("IAP %s = %s, want %s", st, got, want)
		}
	}
	for st, want := range map[string]string{"READY_TO_SUBMIT": "ready", "MISSING_METADATA": "pending", "REJECTED": "pending", "APPROVED": "none"} {
		if got := classify(checkProducts(&statusSnapshot{Subscriptions: map[string]string{"Monthly": st}}), "subscription Monthly", st); got != want {
			t.Errorf("subscription %s = %s, want %s", st, got, want)
		}
	}
}

// ReleaseManager#2/#3: an unversioned READY_TO_SUBMIT IAP is listed and the
// submit plan says it will be attached.
func TestReleaseStatusUnversionedIAPReady(t *testing.T) {
	f := readyFixture()
	f.iapState = "READY_TO_SUBMIT"
	k := setup(t, f)
	out, _ := call(t, k, `{"app":"Hjul","desired":{"submit":true}}`)
	if c := checkByID(t, out, "in_app_products"); !slices.Contains(c.Items, "iap Pro") {
		t.Errorf("in_app_products = %+v", c)
	}
	found := false
	for _, p := range k.Plans.Proposed {
		if strings.HasPrefix(p.Summary, "submit ") {
			b, _ := json.Marshal(p.Intended)
			found = strings.Contains(string(b), "attach iap pro")
		}
	}
	if !found {
		t.Errorf("submit plan does not list the IAP: %+v", k.Plans.Proposed)
	}
}

// ReleaseManager#4: an iOS version without any iPad screenshots is never ok.
func TestReleaseStatusIPadUnknownWarns(t *testing.T) {
	f := readyFixture()
	f.noIPad = true
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	c := checkByID(t, out, "screenshots")
	if c.Status != statusWarn || len(c.Items) != 1 || !strings.Contains(c.Items[0], "APP_IPAD_PRO_3GEN_129 is required") {
		t.Errorf("screenshots = %+v", c)
	}
}

// ReleaseManager#5: unanswered general age rating questions block.
func TestReleaseStatusAgeRatingUnansweredBlocks(t *testing.T) {
	f := readyFixture()
	f.ageUnanswered = true
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "age_rating"); c.Status != statusBlock || !slices.Equal(c.Items, []string{"messagingAndChat"}) || c.FixVia == nil || c.FixVia.Tool != "appinfo_update" {
		t.Errorf("age_rating = %+v", c)
	}
	if ready(out) {
		t.Error("ready with an unanswered age rating question")
	}
}

// AgentUX#4: in read-only mode the release_cancel option says it is locked
// and how to unlock it.
func TestReleaseStatusCancelOptionLocked(t *testing.T) {
	out, _ := call(t, lockedKit(t, false), `{"app":"Hjul"}`)
	o := out.InFlight.Options[0]
	if o.Locked != "writes_disabled" || o.Unlock == nil || o.Unlock.Tool != "toolsets" || o.Unlock.Args["action"] != "enable_writes" {
		t.Errorf("option = %+v", o)
	}
	k := lockedKit(t, false)
	k.Env.Profile.AllowWrites = true
	out, _ = call(t, k, `{"app":"Hjul"}`)
	if o := out.InFlight.Options[0]; o.Locked != "" || o.Unlock != nil {
		t.Errorf("write mode option = %+v", o)
	}
}

// AgentUX#6a: a live version's missing required screenshots are reported
// per locale, as info.
func TestReleaseStatusLiveVersionScreenshotsInfo(t *testing.T) {
	f := readyFixture()
	f.state, f.noIPhoneShots = "READY_FOR_DISTRIBUTION", true
	k := setup(t, f)
	k.Resolve.Versions["live"] = tool.Version{ID: "v1", String: "1.4", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"}
	out, _ := call(t, k, `{"app":"Hjul","version":"live"}`)
	c := checkByID(t, out, "screenshots")
	if c.Status != statusInfo || c.FixVia != nil || !strings.Contains(c.Detail, "not editable") || len(c.Items) != 1 || !strings.Contains(c.Items[0], "en-US: no screenshots for APP_IPHONE_67") {
		t.Errorf("screenshots = %+v", c)
	}
}
