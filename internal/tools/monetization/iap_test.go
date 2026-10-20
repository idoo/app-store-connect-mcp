package monetization

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	iapTestProduct = "se.jvcr.hjul.coins100"
	iapLookup      = "/v1/apps/{id}/inAppPurchasesV2"
)

func iapProductDoc(id, name, note string, fam bool) string {
	return jsonapiDoc(fmt.Sprintf(`[{"type":"inAppPurchases","id":%q,"attributes":{"name":%q,"productId":%q,"inAppPurchaseType":"CONSUMABLE","state":"APPROVED","reviewNote":%q,"familySharable":%t}}]`,
		id, name, iapTestProduct, note, fam))
}

func TestIAPListCounts(t *testing.T) {
	k := newKit(t)
	k.ASC.HandleFile(http.MethodGet, iapLookup, "testdata/catalog/iap_list.json")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_list"), k.Env, `{"type":"CONSUMABLE"}`))
	if n := len(k.ASC.Requests()); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	q := k.ASC.Requests()[0].Query
	if q.Get("filter[inAppPurchaseType]") != "CONSUMABLE" || !strings.Contains(q.Get("include"), "versions") {
		t.Fatalf("query = %s", q.Encode())
	}
	out := tooltest.Decode[iapListOut](t, res)
	if out.Returned != 3 || out.Total != 3 || out.ByState["APPROVED"] != 2 || out.ByState["READY_TO_SUBMIT"] != 1 {
		t.Fatalf("counts = %+v", out)
	}
	first := out.Items[0]
	if first.Version == nil || first.Version.ID != "v1b" || first.Version.State != "PREPARE_FOR_SUBMISSION" {
		t.Fatalf("latest version = %+v", first.Version)
	}
	if len(first.Localizations) != 2 || first.Localizations[0].Locale != "en-US" {
		t.Fatalf("localizations = %+v", first.Localizations)
	}
	if out.Items[2].Version != nil || len(out.Items[2].Localizations) != 0 {
		t.Fatalf("bare item = %+v", out.Items[2])
	}
}

func TestIAPUpsertCreatesFullSequence(t *testing.T) {
	k := newKit(t)
	shot := []byte("\x89PNG fake screenshot bytes")
	if err := os.WriteFile(filepath.Join(k.Files.Root, "review.png"), shot, 0o600); err != nil {
		t.Fatal(err)
	}
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v2/inAppPurchases", http.StatusCreated,
		`{"data":{"type":"inAppPurchases","id":"iapN","attributes":{"productId":"`+iapTestProduct+`","state":"MISSING_METADATA"}}}`)
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/versions", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/inAppPurchaseVersions", http.StatusCreated,
		`{"data":{"type":"inAppPurchaseVersions","id":"verN","attributes":{"version":1,"state":"PREPARE_FOR_SUBMISSION"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/inAppPurchaseVersions/{id}/localizations", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v2/inAppPurchaseLocalizations", http.StatusCreated,
		`{"data":{"type":"inAppPurchaseLocalizations","id":"loc-new"}}`)
	k.ASC.Handle(http.MethodPost, "/v1/inAppPurchaseAppStoreReviewScreenshots", http.StatusCreated, fmt.Sprintf(
		`{"data":{"type":"inAppPurchaseAppStoreReviewScreenshots","id":"shotN","attributes":{"fileName":"review.png","fileSize":%d,
		"uploadOperations":[{"method":"PUT","url":"https://upload.example.com/p0","length":%d,"offset":0,"requestHeaders":[{"name":"Content-Type","value":"image/png"}]}]}}}`,
		len(shot), len(shot)))
	k.Fetch.Handle(http.MethodPut, "https://upload.example.com/p0", http.StatusOK, nil)
	k.ASC.Handle(http.MethodPatch, "/v1/inAppPurchaseAppStoreReviewScreenshots/{id}", http.StatusOK,
		`{"data":{"type":"inAppPurchaseAppStoreReviewScreenshots","id":"shotN","attributes":{"assetDeliveryState":{"state":"COMPLETE"}}}}`)

	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{
		"product_id":"`+iapTestProduct+`","type":"CONSUMABLE","reference_name":"Coins 100","family_sharable":false,
		"localizations":{"en":{"name":"100 coins","description":"A bag of coins"},"Swedish":{"name":"100 mynt"}},
		"review_screenshot":"review.png","confirm":true}`))

	// The approval ask and the approved re-run both look the product up.
	equalLines(t, requestLog(k.ASC.Requests()), []string{
		"GET /v1/apps/app1/inAppPurchasesV2",
		"GET /v1/apps/app1/inAppPurchasesV2",
		"POST /v2/inAppPurchases",
		"GET /v2/inAppPurchases/iapN/versions",
		"POST /v1/inAppPurchaseVersions",
		"GET /v1/inAppPurchaseVersions/verN/localizations",
		"POST /v2/inAppPurchaseLocalizations",
		"POST /v2/inAppPurchaseLocalizations",
		"POST /v1/inAppPurchaseAppStoreReviewScreenshots",
		"PATCH /v1/inAppPurchaseAppStoreReviewScreenshots/shotN",
	})
	reqs := k.ASC.Requests()[1:]
	if a := reqAttrs(t, reqs[1]); a["productId"] != iapTestProduct || a["name"] != "Coins 100" || a["inAppPurchaseType"] != "CONSUMABLE" {
		t.Fatalf("create attrs = %v", a)
	}
	if got := reqRel(t, reqs[1], "app"); got != "apps/app1" {
		t.Fatalf("app rel = %s", got)
	}
	if got := reqRel(t, reqs[3], "inAppPurchase"); got != "inAppPurchases/iapN" {
		t.Fatalf("version rel = %s", got)
	}
	for _, r := range reqs[5:7] {
		if got := reqRel(t, r, "version"); got != "inAppPurchaseVersions/verN" {
			t.Fatalf("localization must attach to the version, got %s", got)
		}
	}
	if a := reqAttrs(t, reqs[5]); a["locale"] != "en-US" || a["description"] != "A bag of coins" {
		t.Fatalf("en-US attrs = %v", a)
	}
	if a := reqAttrs(t, reqs[6]); a["locale"] != "sv" {
		t.Fatalf("sv attrs = %v", a)
	}
	if len(k.Fetch.Calls) != 1 {
		t.Fatalf("fetch calls = %v", k.Fetch.Calls)
	}
	out := tooltest.Decode[iapUpsertOut](t, res)
	if out.Product.Action != actionCreated || out.Version == nil || !out.Version.Created || out.ReviewScreenshot.Action != actionCreated || out.Failed != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestIAPUpsertExistingPatchesOnlyDiff(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Old name", "note", false))
	k.ASC.Handle(http.MethodPatch, "/v2/inAppPurchases/{id}", http.StatusOK, `{"data":{"type":"inAppPurchases","id":"iap1"}}`)
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/versions", http.StatusOK, jsonapiDoc(
		`[{"type":"inAppPurchaseVersions","id":"ver1","attributes":{"version":2,"state":"PREPARE_FOR_SUBMISSION"},
		 "relationships":{"localizations":{"data":[{"type":"inAppPurchaseLocalizations","id":"loc-en"},{"type":"inAppPurchaseLocalizations","id":"loc-fr"}]}}}]`,
		`{"type":"inAppPurchaseLocalizations","id":"loc-en","attributes":{"locale":"en-US","name":"100 coins","description":"A bag"}}`,
		`{"type":"inAppPurchaseLocalizations","id":"loc-fr","attributes":{"locale":"fr-FR","name":"100 pièces"}}`))
	k.ASC.Handle(http.MethodPatch, "/v2/inAppPurchaseLocalizations/{id}", http.StatusOK, `{"data":{"type":"inAppPurchaseLocalizations","id":"loc-en"}}`)
	k.ASC.Handle(http.MethodPost, "/v2/inAppPurchaseLocalizations", http.StatusCreated, `{"data":{"type":"inAppPurchaseLocalizations","id":"loc-de"}}`)

	const args = `"product_id":"` + iapTestProduct + `","type":"CONSUMABLE","reference_name":"New name","review_note":"note",
		"localizations":{"en-US":{"name":"100 coins","description":"A bag of coins"},"de-DE":{"name":"100 Münzen"}}`
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{`+args+`}`))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{`+args+`,"confirm":true}`))

	equalLines(t, requestLog(k.ASC.Mutations()), []string{
		"PATCH /v2/inAppPurchases/iap1",
		"POST /v2/inAppPurchaseLocalizations",
		"PATCH /v2/inAppPurchaseLocalizations/loc-en",
	})
	if len(k.ASC.RequestsTo(http.MethodPost, "/v2/inAppPurchases")) != 0 {
		t.Fatal("an existing product id must never be POSTed again")
	}
	m := k.ASC.Mutations()
	if a := reqAttrs(t, m[0]); len(a) != 1 || a["name"] != "New name" {
		t.Fatalf("product PATCH must carry only the diff, got %v", a)
	}
	if a := reqAttrs(t, m[2]); len(a) != 1 || a["description"] != "A bag of coins" {
		t.Fatalf("localization PATCH must carry only the diff, got %v", a)
	}
	out := tooltest.Decode[iapUpsertOut](t, res)
	if out.Product.Action != actionUpdated || strings.Join(out.Product.Changed, ",") != "name" || out.Version.Created {
		t.Fatalf("out = %+v", out)
	}
	// fr-FR is not in the request and is never touched.
	for _, it := range out.Localizations {
		if it.Locale == "fr-FR" {
			t.Fatalf("untouched locale reported: %+v", it)
		}
	}
}

// When every write fails the upsert is an error whose items carry each code.
func TestIAPUpsertAllFailedIsError(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", false))
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/versions", http.StatusOK, jsonapiDoc(
		`[{"type":"inAppPurchaseVersions","id":"ver1","attributes":{"version":2,"state":"PREPARE_FOR_SUBMISSION"},
		 "relationships":{"localizations":{"data":[{"type":"inAppPurchaseLocalizations","id":"loc-en"}]}}}]`,
		`{"type":"inAppPurchaseLocalizations","id":"loc-en","attributes":{"locale":"en-US","name":"100 coins","description":"A bag"}}`))
	const invalid = `{"errors":[{"status":"422","code":"ENTITY_ERROR.ATTRIBUTE.INVALID","title":"bad"}]}`
	k.ASC.Handle(http.MethodPatch, "/v2/inAppPurchaseLocalizations/{id}", http.StatusUnprocessableEntity, invalid)
	k.ASC.Handle(http.MethodPost, "/v2/inAppPurchaseLocalizations", http.StatusUnprocessableEntity, invalid)

	const args = `"product_id":"` + iapTestProduct + `","reference_name":"Coins","review_note":"note",
		"localizations":{"en-US":{"name":"100 coins","description":"A bag of coins"},"de-DE":{"name":"100 Münzen"}}`
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{`+args+`}`))
	res := tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{`+args+`,"confirm":true}`)
	if !res.IsError {
		t.Fatalf("want error, got %s", tooltest.JSON(t, res.Data))
	}
	body := tooltest.Decode[struct {
		Error string       `json:"error"`
		Items iapUpsertOut `json:"items"`
	}](t, res)
	if body.Error != "api_error" || body.Items.Failed != 2 || len(body.Items.Localizations) != 2 {
		t.Fatalf("body = %+v", body)
	}
	for _, it := range body.Items.Localizations {
		if it.Outcome != "error" || it.Error == nil || it.Error.Code != "ENTITY_ERROR.ATTRIBUTE.INVALID" {
			t.Errorf("item %s = %+v", it.Locale, it)
		}
	}
}

func TestIAPUpsertUnchangedIsAlready(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", true))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env,
		`{"product_id":"`+iapTestProduct+`","reference_name":"Coins","family_sharable":true}`))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("mutations = %v", requestLog(k.ASC.Mutations()))
	}
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true {
		t.Fatalf("want already, got %v", d)
	}
}

func TestIAPUpsertConflictReadsBack(t *testing.T) {
	k := newKit(t)
	// Approval ask and approved re-run both miss; the 409 read-back finds it.
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iapR", "Coins", "", false))
	k.ASC.Handle(http.MethodPost, "/v2/inAppPurchases", http.StatusConflict, conflictBody)
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env,
		`{"product_id":"`+iapTestProduct+`","type":"CONSUMABLE","reference_name":"Coins","confirm":true}`))
	equalLines(t, requestLog(k.ASC.Requests()), []string{
		"GET /v1/apps/app1/inAppPurchasesV2",
		"GET /v1/apps/app1/inAppPurchasesV2",
		"POST /v2/inAppPurchases",
		"GET /v1/apps/app1/inAppPurchasesV2",
	})
	d := tooltest.Decode[map[string]any](t, res)
	if d["already"] != true {
		t.Fatalf("want already, got %v", d)
	}
	prod := d["product"].(map[string]any)
	if prod["id"] != "iapR" || prod["action"] != actionAlready {
		t.Fatalf("product = %v", prod)
	}
}

func TestIAPUpsertRejectsBeforeRequests(t *testing.T) {
	cases := map[string]string{
		"display name": `{"product_id":"p.x","localizations":{"en-US":{"name":"` + strings.Repeat("n", 31) + `"}}}`,
		"description":  `{"product_id":"p.x","localizations":{"en-US":{"name":"ok","description":"` + strings.Repeat("d", 46) + `"}}}`,
		"reference":    `{"product_id":"p.x","reference_name":"` + strings.Repeat("r", 65) + `"}`,
		"locale":       `{"product_id":"p.x","localizations":{"Klingon":{"name":"ok"}}}`,
		"product id":   `{"product_id":"bad id!"}`,
		"screenshot":   `{"product_id":"p.x","review_screenshot":"missing.png"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			res := tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args)
			if !res.IsError {
				t.Fatalf("want error, got %s", tooltest.JSON(t, res.Data))
			}
			if n := len(k.ASC.Requests()); n != 0 {
				t.Fatalf("%d requests sent before validation", n)
			}
		})
	}
}

func TestIAPUpsertCreateNeedsTypeAndFamilyNeverOff(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	if res := tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{"product_id":"`+iapTestProduct+`"}`); !res.IsError {
		t.Fatal("create without type must fail")
	}
	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "", true))
	if res := tooltest.Call(t, mustTool(t, "iap_upsert"), k2.Env, `{"product_id":"`+iapTestProduct+`","family_sharable":false}`); !res.IsError {
		t.Fatal("turning family sharing off must fail")
	}
	if len(k.ASC.Mutations())+len(k2.ASC.Mutations()) != 0 {
		t.Fatal("no mutation expected")
	}
}

func TestIAPUpsertReadOnlyRefused(t *testing.T) {
	k := newKit(t).ReadOnly()
	if res := tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, `{"product_id":"p.x"}`); !res.IsError {
		t.Fatal("read-only mode must refuse iap_upsert")
	}
}
