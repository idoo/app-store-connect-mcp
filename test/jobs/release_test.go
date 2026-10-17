package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// res is one JSON:API resource; Rels values are "type:id" or []"type:id".
type res struct {
	Type, ID string
	Attrs    map[string]any
	Rels     map[string]any
}

func (r res) raw() map[string]any {
	m := map[string]any{"type": r.Type, "id": r.ID, "attributes": r.Attrs}
	if len(r.Rels) == 0 {
		return m
	}
	ident := func(s string) map[string]any {
		typ, id, _ := strings.Cut(s, ":")
		return map[string]any{"type": typ, "id": id}
	}
	rels := map[string]any{}
	for name, v := range r.Rels {
		switch v := v.(type) {
		case string:
			rels[name] = map[string]any{"data": ident(v)}
		case []string:
			data := []any{}
			for _, s := range v {
				data = append(data, ident(s))
			}
			rels[name] = map[string]any{"data": data}
		}
	}
	m["relationships"] = rels
	return m
}

// jsonDoc renders a JSON:API document with data (res or []res) and included.
func jsonDoc(t *testing.T, data any, included ...res) []byte {
	t.Helper()
	d := map[string]any{}
	switch v := data.(type) {
	case res:
		d["data"] = v.raw()
	case []res:
		list := []any{}
		for _, r := range v {
			list = append(list, r.raw())
		}
		d["data"] = list
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
	return b
}

// reqBody returns a request's JSON:API data object.
func reqBody(r tool.Request) map[string]any {
	b, _ := json.Marshal(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	d, _ := m["data"].(map[string]any)
	return d
}

// releaseFake is editable version 1.4 of Hjul with three locales and build
// 42 attached; prepare's PATCHes update it so the submit plan, bound to the
// post-prepare state, verifies.
type releaseFake struct {
	mu       sync.Mutex
	buildID  string
	whatsNew map[string]string // localization id -> What's New
}

var releaseLocales = map[string]string{"loc-en": "en-US", "loc-de": "de-DE", "loc-ja": "ja"}

func releaseKit(t *testing.T) (*tooltest.Kit, *releaseFake) {
	t.Helper()
	k := newKit(t)
	v14 := tool.Version{ID: "v1", String: "1.4", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{tool.VersionEditable: v14, "1.4": v14}
	k.Resolve.Builds = map[tool.BuildRef]tool.Build{tool.BuildLatest: {ID: "b2", Number: "43", ProcessingState: "VALID"}, "latest:1.4": {ID: "b2", Number: "43", ProcessingState: "VALID"}}
	f := &releaseFake{buildID: "b1", whatsNew: map[string]string{"loc-en": "Bug fixes.", "loc-de": "Fehlerbehebungen.", "loc-ja": "バグ修正。"}}
	asc := k.ASC

	prior := res{Type: "appStoreVersions", ID: "v0", Attrs: map[string]any{"platform": "IOS", "versionString": "1.3", "appVersionState": "READY_FOR_DISTRIBUTION", "releaseType": "AFTER_APPROVAL"}}
	cur := res{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"platform": "IOS", "versionString": "1.4", "appVersionState": "PREPARE_FOR_SUBMISSION", "releaseType": "AFTER_APPROVAL"}}
	asc.Handle(http.MethodGet, "/v1/apps/{id}", http.StatusOK, string(jsonDoc(t, res{
		Type: "apps", ID: hjul.ID,
		Attrs: map[string]any{"primaryLocale": "en-US", "contentRightsDeclaration": "DOES_NOT_USE_THIRD_PARTY_CONTENT"},
		Rels:  map[string]any{"appStoreVersions": []string{"appStoreVersions:v1", "appStoreVersions:v0"}},
	}, cur, prior)))

	locIDs := []string{"loc-de", "loc-en", "loc-ja"}
	asc.HandleFunc(http.MethodGet, "/v1/appStoreVersions/{id}", func(tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		var inc []res
		var locRels []string
		for _, id := range locIDs {
			locRels = append(locRels, "appStoreVersionLocalizations:"+id)
			inc = append(inc, res{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{
				"locale": releaseLocales[id], "description": "A sailing app.", "keywords": "boat,sail,wind",
				"supportUrl": "https://jvcr.se/support", "whatsNew": f.whatsNew[id], "promotionalText": "Sail on.",
			}})
		}
		inc = append(inc,
			res{Type: "appStoreReviewDetails", ID: "rd1", Attrs: map[string]any{"contactFirstName": "Ivan", "contactLastName": "D", "contactPhone": "+46700000000", "contactEmail": "a@b.se", "demoAccountRequired": false}},
			res{Type: "builds", ID: f.buildID, Attrs: map[string]any{"version": map[string]string{"b1": "42", "b2": "43"}[f.buildID], "processingState": "VALID", "expired": false, "usesNonExemptEncryption": false}},
		)
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, res{
			Type: "appStoreVersions", ID: "v1", Attrs: cur.Attrs,
			Rels: map[string]any{"appStoreVersionLocalizations": locRels, "appStoreReviewDetail": "appStoreReviewDetails:rd1", "build": "builds:" + f.buildID},
		}, inc...)}
	})

	var infoRels []string
	var infoInc []res
	for _, id := range locIDs {
		infoRels = append(infoRels, "appInfoLocalizations:info-"+id)
		infoInc = append(infoInc, res{Type: "appInfoLocalizations", ID: "info-" + id, Attrs: map[string]any{"locale": releaseLocales[id], "name": "Hjul", "subtitle": "Sail better", "privacyPolicyUrl": "https://jvcr.se/privacy"}})
	}
	infoInc = append(infoInc, res{Type: "ageRatingDeclarations", ID: "age1", Attrs: map[string]any{"violenceCartoonOrFantasy": "NONE", "kidsAgeBand": nil}})
	asc.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, string(jsonDoc(t, []res{{
		Type: "appInfos", ID: "ai1", Attrs: map[string]any{"state": "PREPARE_FOR_SUBMISSION"},
		Rels: map[string]any{"appInfoLocalizations": infoRels, "ageRatingDeclaration": "ageRatingDeclarations:age1"},
	}}, infoInc...)))
	asc.HandleFunc(http.MethodGet, "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets", func(r tool.Request) tooltest.Fixture {
		id := strings.Split(r.Path, "/")[3]
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t,
			[]res{{Type: "appScreenshotSets", ID: "set-" + id, Attrs: map[string]any{"screenshotDisplayType": "APP_IPHONE_67"}, Rels: map[string]any{"appScreenshots": []string{"appScreenshots:s-" + id}}}},
			res{Type: "appScreenshots", ID: "s-" + id, Attrs: map[string]any{"assetDeliveryState": map[string]any{"state": "COMPLETE"}}})}
	})
	asc.Handle(http.MethodGet, "/v1/apps/{id}/appPriceSchedule", http.StatusOK, string(jsonDoc(t, res{Type: "appPriceSchedules", ID: hjul.ID, Attrs: map[string]any{}, Rels: map[string]any{"baseTerritory": "territories:USA"}})))
	asc.Handle(http.MethodGet, "/v1/apps/{id}/appAvailabilityV2", http.StatusOK, string(jsonDoc(t, res{Type: "appAvailabilities", ID: hjul.ID, Attrs: map[string]any{"availableInNewTerritories": true}})))
	asc.Handle(http.MethodGet, "/v1/reviewSubmissions", http.StatusOK, `{"data":[]}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}/inAppPurchasesV2", http.StatusOK, `{"data":[]}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, `{"data":[]}`)
	asc.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, `{"data":[]}`)
	asc.Handle(http.MethodGet, "/v1/builds/{id}", http.StatusOK, string(jsonDoc(t, res{Type: "builds", ID: "b2", Attrs: map[string]any{"version": "43", "processingState": "VALID", "expired": false, "usesNonExemptEncryption": false}})))
	asc.Handle(http.MethodGet, "/v1/builds/{id}/preReleaseVersion", http.StatusOK, string(jsonDoc(t, res{Type: "preReleaseVersions", ID: "prv14", Attrs: map[string]any{"version": "1.4", "platform": "IOS"}})))
	asc.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, string(jsonDoc(t, []res{cur})))

	// release_prepare
	asc.HandleFunc(http.MethodPatch, "/v1/appStoreVersions/{id}/relationships/build", func(r tool.Request) tooltest.Fixture {
		b := reqBody(r)
		f.mu.Lock()
		f.buildID, _ = b["id"].(string)
		f.mu.Unlock()
		return tooltest.Fixture{Status: http.StatusNoContent}
	})
	asc.HandleFunc(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", func(r tool.Request) tooltest.Fixture {
		id := strings.Split(r.Path, "/")[3]
		attrs, _ := reqBody(r)["attributes"].(map[string]any)
		wn, _ := attrs["whatsNew"].(string)
		f.mu.Lock()
		f.whatsNew[id] = wn
		f.mu.Unlock()
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, res{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{"locale": releaseLocales[id], "whatsNew": wn}})}
	})

	// release_submit
	asc.Handle(http.MethodPost, "/v1/reviewSubmissions", http.StatusCreated, string(jsonDoc(t, res{Type: "reviewSubmissions", ID: "rs9", Attrs: map[string]any{"platform": "IOS", "state": "READY_FOR_REVIEW"}})))
	asc.Handle(http.MethodPost, "/v1/reviewSubmissionItems", http.StatusCreated, string(jsonDoc(t, res{Type: "reviewSubmissionItems", ID: "it9", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}})))
	asc.Handle(http.MethodPatch, "/v1/reviewSubmissions/{id}", http.StatusOK, string(jsonDoc(t, res{Type: "reviewSubmissions", ID: "rs9", Attrs: map[string]any{"state": "WAITING_FOR_REVIEW"}})))
	asc.Handle(http.MethodGet, "/v1/reviewSubmissions/{id}", http.StatusOK, string(jsonDoc(t, res{Type: "reviewSubmissions", ID: "rs9", Attrs: map[string]any{"state": "WAITING_FOR_REVIEW"}})))
	return k, f
}

// Job 1: "release 1.4 of Hjul with notes in 3 locales" in at most 4 calls:
// one Read preview with the desired release, then the two plan-confirmed
// mutations. Refs only: the app by name, the build as latest.
func TestJobReleaseWithNotesInThreeLocales(t *testing.T) {
	k, f := releaseKit(t)
	notes := map[string]string{"en-US": "New sails.", "de-DE": "Neue Segel.", "ja": "新しい帆。"}
	wn, err := json.Marshal(notes)
	if err != nil {
		t.Fatal(err)
	}

	j := connect(t, k)
	status := j.call("release_status", fmt.Sprintf(`{"app":"Hjul","version":"1.4","desired":{"build":"latest","whats_new":%s,"submit":true}}`, wn))
	plan, _ := status["plan"].(map[string]any)
	prepareID, _ := plan["prepare_plan_id"].(string)
	submitID, _ := plan["submit_plan_id"].(string)
	if prepareID == "" || submitID == "" {
		t.Fatalf("release_status returned no plan ids: %v", status)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("release_status sent %d mutations", n)
	}
	j.call("release_prepare", fmt.Sprintf(`{"app":"Hjul","version":"1.4","build":"latest","whats_new":%s,"plan_id":%q}`, wn, prepareID))
	sub := j.call("release_submit", fmt.Sprintf(`{"app":"Hjul","version":"1.4","plan_id":%q}`, submitID))
	j.done(4)
	if j.calls[0].class != tool.Read {
		t.Errorf("first call %s is class %v, want Read", j.calls[0].name, j.calls[0].class)
	}
	if s, _ := sub["submission"].(map[string]any); s["state"] != "WAITING_FOR_REVIEW" {
		t.Errorf("submission = %v", sub["submission"])
	}

	var got []string
	for _, m := range k.ASC.Mutations() {
		got = append(got, m.Method+" "+m.Path)
	}
	if slices.Contains(got, "POST /v1/appStoreVersions") {
		t.Errorf("version 1.4 exists but was created again: %v", got)
	}
	for _, want := range []string{
		"PATCH /v1/appStoreVersions/v1/relationships/build",
		"PATCH /v1/appStoreVersionLocalizations/loc-en",
		"PATCH /v1/appStoreVersionLocalizations/loc-de",
		"PATCH /v1/appStoreVersionLocalizations/loc-ja",
		"POST /v1/reviewSubmissions",
		"POST /v1/reviewSubmissionItems",
		"PATCH /v1/reviewSubmissions/rs9",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing mutation %q in %v", want, got)
		}
	}
	f.mu.Lock()
	if f.buildID != "b2" {
		t.Errorf("attached build = %s, want latest b2", f.buildID)
	}
	for id, loc := range releaseLocales {
		if f.whatsNew[id] != notes[loc] {
			t.Errorf("What's New %s = %q, want %q", loc, f.whatsNew[id], notes[loc])
		}
	}
	f.mu.Unlock()
	for _, m := range k.ASC.RequestsTo(http.MethodPatch, "/v1/reviewSubmissions/{id}") {
		if attrs, _ := reqBody(m)["attributes"].(map[string]any); attrs["submitted"] != true {
			t.Errorf("submission PATCH lacks submitted:true: %v", attrs)
		}
	}
	for _, m := range k.ASC.RequestsTo(http.MethodPost, "/v1/reviewSubmissionItems") {
		rels, _ := reqBody(m)["relationships"].(map[string]any)
		if v, _ := rels["appStoreVersion"].(map[string]any); v == nil {
			t.Errorf("submission item does not carry the version: %v", rels)
		}
	}
}
