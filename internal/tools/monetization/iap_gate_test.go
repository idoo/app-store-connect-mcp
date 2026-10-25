package monetization

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool/tooltest"
)

// Creating an IAP burns its product id: the agent's confirm alone is refused
// and nothing is written; a preview call writes nothing either.
func TestIAPUpsertCreateNeedsHuman(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	args := `{"product_id":"` + iapTestProduct + `","type":"CONSUMABLE","reference_name":"Coins"`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`}`))
	if d := tooltest.Decode[map[string]any](t, res); d["status"] != "needs_confirm" {
		t.Fatalf("want preview, got %v", d)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`,"confirm":true}`), "human_approval_required")
	k.Elicit(false)
	if res := tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`,"confirm":true}`); !res.IsError {
		t.Fatal("declined approval must refuse")
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
}

func TestSubscriptionsUpsertCreateNeedsHuman(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(`[]`))
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","name":"Yearly","confirm":true}`), "human_approval_required")
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
}

// Turning family sharing on is one-way: confirm alone is refused, a human
// approves, and the PATCH carries only that field.
func TestIAPUpsertFamilySharingOnNeedsHuman(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", false))
	k.ASC.Handle(http.MethodPatch, "/v2/inAppPurchases/{id}", http.StatusOK, `{"data":{"type":"inAppPurchases","id":"iap1"}}`)
	args := `{"product_id":"` + iapTestProduct + `","reference_name":"Coins","family_sharable":true,"confirm":true}`
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args), "human_approval_required")
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
	asked := k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args))
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "family sharing") {
		t.Fatalf("approval prompt = %v", *asked)
	}
	m := k.ASC.Mutations()
	if len(m) != 1 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
	if a := reqAttrs(t, m[0]); len(a) != 1 || a["familySharable"] != true {
		t.Fatalf("PATCH attrs = %v", a)
	}
}

// Group level changes on an existing subscription need a human.
func TestSubscriptionsUpsertLevelChangeNeedsHuman(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptions","id":"s1","attributes":{"name":"Yearly","productId":"`+subsTestProduct+`","groupLevel":2}}]`))
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","group_level":1,"confirm":true}`), "human_approval_required")
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
}

// Overwriting a non-empty field needs confirm:true; the preview writes nothing.
func TestIAPUpsertOverwriteNeedsConfirm(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", false))
	k.ASC.Handle(http.MethodPatch, "/v2/inAppPurchases/{id}", http.StatusOK, `{"data":{"type":"inAppPurchases","id":"iap1"}}`)
	args := `{"product_id":"` + iapTestProduct + `","reference_name":"Coins","review_note":"new note"`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`}`))
	if d := tooltest.Decode[map[string]any](t, res); d["status"] != "needs_confirm" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("want preview without writes, got %v", d)
	}
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`,"confirm":true}`))
	m := k.ASC.Mutations()
	if len(m) != 1 {
		t.Fatalf("mutations = %v", requestLog(m))
	}
	// Unchanged name is not re-PATCHed.
	if a := reqAttrs(t, m[0]); len(a) != 1 || a["reviewNote"] != "new note" {
		t.Fatalf("PATCH attrs = %v", a)
	}
}

// A first availability is additive (no confirm) and its body matches the
// spec's create request; the fake ASC validates it.
func TestIAPUpsertSetsAvailability(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", false))
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/inAppPurchaseAvailability", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/inAppPurchaseAvailabilities", http.StatusCreated,
		`{"data":{"type":"inAppPurchaseAvailabilities","id":"av1"}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env,
		`{"product_id":"`+iapTestProduct+`","availability":{"territories":["swe","USA"],"available_in_new_territories":true}}`))
	m := k.ASC.Mutations()
	if len(m) != 1 || m[0].Path != "/v1/inAppPurchaseAvailabilities" {
		t.Fatalf("mutations = %v", requestLog(m))
	}
	if a := reqAttrs(t, m[0]); a["availableInNewTerritories"] != true {
		t.Fatalf("attrs = %v", a)
	}
	if got := reqRel(t, m[0], "inAppPurchase"); got != "inAppPurchases/iap1" {
		t.Fatalf("rel = %s", got)
	}
	rels := bodyOf(t, m[0])["data"].(map[string]any)["relationships"].(map[string]any)
	var ids []string
	for _, d := range rels["availableTerritories"].(map[string]any)["data"].([]any) {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"SWE", "USA"}) {
		t.Fatalf("territories = %v", ids)
	}
	if out := tooltest.Decode[iapUpsertOut](t, res); out.Availability == nil || out.Availability.Action != actionCreated {
		t.Fatalf("out = %+v", out)
	}
}

// Replacing an existing subscription availability needs confirm; an
// identical one writes nothing.
func TestSubscriptionsUpsertAvailability(t *testing.T) {
	setup := func() *tooltest.Kit {
		k := newKit(t)
		k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
			jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
		k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(
			`[{"type":"subscriptions","id":"s1","attributes":{"name":"Yearly","productId":"`+subsTestProduct+`"}}]`))
		k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/subscriptionAvailability", http.StatusOK,
			`{"data":{"type":"subscriptionAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
		k.ASC.Handle(http.MethodGet, "/v1/subscriptionAvailabilities/{id}/availableTerritories", http.StatusOK,
			jsonapiDoc(`[{"type":"territories","id":"USA"},{"type":"territories","id":"SWE"}]`))
		k.ASC.Handle(http.MethodPost, "/v1/subscriptionAvailabilities", http.StatusCreated,
			`{"data":{"type":"subscriptionAvailabilities","id":"av2"}}`)
		return k
	}
	k := setup()
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","availability":{"territories":["USA","SWE"]}}`))
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("identical availability must not write, got %v", d)
	}

	k = setup()
	args := `{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{"territories":["USA"]}`
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, args+`}`))
	if d := tooltest.Decode[map[string]any](t, res); d["status"] != "needs_confirm" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("want preview, got %v", d)
	}
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, args+`,"confirm":true}`))
	m := k.ASC.Mutations()
	if len(m) != 1 || m[0].Path != "/v1/subscriptionAvailabilities" || reqRel(t, m[0], "subscription") != "subscriptions/s1" {
		t.Fatalf("mutations = %v", requestLog(m))
	}
	if a := reqAttrs(t, m[0]); a["availableInNewTerritories"] != true {
		t.Fatalf("current available_in_new_territories must be kept, got %v", a)
	}
}

func TestIAPListPointsToPricing(t *testing.T) {
	k := newKit(t)
	k.ASC.HandleFile(http.MethodGet, iapLookup, "testdata/catalog/iap_list.json")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_list"), k.Env, `{}`))
	js := tooltest.JSON(t, res)
	if !strings.Contains(js, `"tool":"pricing_get"`) || !strings.Contains(js, `"products":[`) {
		t.Fatalf("want pricing_get next step with products, got %s", js)
	}
}
