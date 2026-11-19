package monetization

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// holderOnly409 is Apple's answer when an API key removes the availability of
// an on-sale subscription.
const holderOnly409 = `{"errors":[{"status":"409","code":"STATE_ERROR.INVALID_REQUEST_SUBSCRIPTION_AVAILABILITY_REMOVAL_NOT_ALLOWED",` +
	`"title":"Subscription availability cannot be removed.",` +
	`"detail":"Only account holder can remove availability for subscription with state (IAP_STATE_READY_FOR_SALE)."}]}`

const removeSubArgs = `{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{"remove_from_sale":true}`

// removeSubKit is an on-sale subscription in USA, SWE and DEU whose
// availability POST the API key is refused (Account Holder only).
func removeSubKit(t *testing.T, terrs string, newTerrs bool) *tooltest.Kit {
	t.Helper()
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptions","id":"s1","attributes":{"name":"Yearly","productId":"`+subsTestProduct+`"}}]`))
	if newTerrs {
		k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/subscriptionAvailability", http.StatusOK,
			`{"data":{"type":"subscriptionAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
	} else {
		k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/subscriptionAvailability", http.StatusOK,
			`{"data":{"type":"subscriptionAvailabilities","id":"av1","attributes":{"availableInNewTerritories":false}}}`)
	}
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionAvailabilities/{id}/availableTerritories", http.StatusOK, jsonapiDoc(terrs))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionAvailabilities", http.StatusConflict, holderOnly409)
	return k
}

const threeTerrs = `[{"type":"territories","id":"USA"},{"type":"territories","id":"SWE"},{"type":"territories","id":"DEU"}]`

// checkRemovalBody asserts a full-removal availability POST.
func checkRemovalBody(t *testing.T, r tool.Request, rel, want string) {
	t.Helper()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.Path, "Availabilities") {
		t.Fatalf("request = %s %s", r.Method, r.Path)
	}
	if a := reqAttrs(t, r); a["availableInNewTerritories"] != false {
		t.Fatalf("attrs = %v", a)
	}
	rels := bodyOf(t, r)["data"].(map[string]any)["relationships"].(map[string]any)
	if d, ok := rels["availableTerritories"].(map[string]any)["data"].([]any); !ok || len(d) != 0 {
		t.Fatalf("availableTerritories = %v", rels["availableTerritories"])
	}
	if got := reqRel(t, r, rel); got != want {
		t.Fatalf("rel = %s", got)
	}
}

// The API key is refused for an on-sale subscription; the identical request
// goes once through the web session and the item says so.
func TestSubscriptionsRemoveFromSaleViaWeb(t *testing.T) {
	k := removeSubKit(t, threeTerrs, true).WebSession()
	k.Web.Handle(http.MethodPost, "/v1/subscriptionAvailabilities", http.StatusCreated,
		`{"data":{"type":"subscriptionAvailabilities","id":"av2"}}`)
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`,"confirm":true}`))
	if m := k.ASC.Mutations(); len(m) != 1 {
		t.Fatalf("api mutations = %v", requestLog(m))
	}
	wm := k.Web.Mutations()
	if len(wm) != 1 {
		t.Fatalf("web mutations = %v", requestLog(wm))
	}
	checkRemovalBody(t, wm[0], "subscription", "subscriptions/s1")
	out := tooltest.Decode[subsUpsertOut](t, res)
	if a := out.Availability; a == nil || a.Action != actionUpdated || a.ID != "av2" ||
		a.Note != "0 territories, via web session (Account Holder only)" {
		t.Fatalf("availability = %+v", out.Availability)
	}
}

// batchFail returns the dominant error body of an all-items-failed upsert.
func batchFail(t *testing.T, res *tool.Result, code string) tool.ErrorBody {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected error %q, got success: %s", code, tooltest.JSON(t, res.Data))
	}
	b := tooltest.Decode[tool.ErrorBody](t, res)
	if b.Error != code {
		t.Fatalf("error code = %q (%s), want %q", b.Error, b.Message, code)
	}
	return b
}

// Without a web session the item fails, keeps Apple's message, explains the
// Account Holder rule and points to web_login.
func TestSubscriptionsRemoveFromSaleNoWebSession(t *testing.T) {
	k := removeSubKit(t, threeTerrs, true)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`,"confirm":true}`)
	b := batchFail(t, res, tool.ErrWebSessionRequired.Code)
	for _, s := range []string{"Only account holder can remove availability", "only the Account Holder", "web_login", "Remove from Sale"} {
		if !strings.Contains(b.Message, s) {
			t.Fatalf("message lacks %q: %s", s, b.Message)
		}
	}
	found := false
	for _, n := range res.Next {
		found = found || n.Tool == "web_login"
	}
	if !found {
		t.Fatalf("next = %+v", res.Next)
	}
	if len(k.Web.Mutations()) != 0 {
		t.Fatal("no web session: nothing may reach the web fake")
	}
}

// The web session refuses too: the signed-in Apple ID is not the Account Holder.
func TestSubscriptionsRemoveFromSaleWebNotHolder(t *testing.T) {
	k := removeSubKit(t, threeTerrs, true).WebSession()
	k.Web.Handle(http.MethodPost, "/v1/subscriptionAvailabilities", http.StatusConflict, holderOnly409)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`,"confirm":true}`)
	b := batchFail(t, res, "api_error")
	if !strings.Contains(b.Message, "not the Account Holder either") {
		t.Fatalf("message = %s", b.Message)
	}
	if len(k.Web.Mutations()) != 1 {
		t.Fatalf("web must be tried once, got %v", requestLog(k.Web.Mutations()))
	}
}

// Already removed (no territory, no new territories): nothing to write.
func TestSubscriptionsRemoveFromSaleAlreadyRemoved(t *testing.T) {
	k := removeSubKit(t, `[]`, false)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`}`))
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true {
		t.Fatalf("want already, got %v", d)
	}
	if len(k.ASC.Mutations()) != 0 || len(k.Web.Mutations()) != 0 {
		t.Fatalf("mutations = %v", requestLog(k.ASC.Mutations()))
	}
}

func TestRemoveFromSaleArgs(t *testing.T) {
	for _, args := range []string{
		`{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{"remove_from_sale":true,"territories":["USA"]}}`,
		`{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{"remove_from_sale":true,"all":true}}`,
		`{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{"remove_from_sale":true,"available_in_new_territories":true}}`,
		`{"group":"Hjul Plus","product_id":"` + subsTestProduct + `","availability":{}}`,
	} {
		k := newKit(t)
		tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, args), tool.ErrInvalidArgs.Code)
		if n := len(k.ASC.Requests()); n != 0 {
			t.Fatalf("%s: %d requests before validation", args, n)
		}
	}
	// A product that does not exist yet is on sale nowhere.
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(`[]`))
	b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env,
		`{"product_id":"`+iapTestProduct+`","type":"CONSUMABLE","reference_name":"Coins","availability":{"remove_from_sale":true}}`),
		tool.ErrInvalidArgs.Code)
	if !strings.Contains(b.Message, "does not exist yet") {
		t.Fatalf("message = %s", b.Message)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("mutations = %v", requestLog(k.ASC.Mutations()))
	}
}

// The approval preview of a full removal counts the territories instead of
// listing them, and states what discontinuing a subscription means.
func TestSubscriptionsRemoveFromSalePreview(t *testing.T) {
	k := removeSubKit(t, threeTerrs, true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`}`))
	js := tooltest.JSON(t, res)
	for _, s := range []string{"removes it from sale in all 3 territories", "existing subscribers cannot renew", "end of their current billing period"} {
		if !strings.Contains(js, s) {
			t.Fatalf("preview lacks %q: %s", s, js)
		}
	}
	for _, code := range []string{"USA", "SWE", "DEU"} {
		if strings.Contains(js, code) {
			t.Fatalf("preview lists %s: %s", code, js)
		}
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, removeSubArgs+`,"confirm":true}`), tool.ErrHumanRequired.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("refused call mutated: %v", requestLog(k.ASC.Mutations()))
	}
}

// An IAP removal refused to the API key also goes through the web session.
func TestIAPRemoveFromSaleViaWeb(t *testing.T) {
	k := newKit(t).WebSession()
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "note", false))
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/inAppPurchaseAvailability", http.StatusOK,
		`{"data":{"type":"inAppPurchaseAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/inAppPurchaseAvailabilities/{id}/availableTerritories", http.StatusOK, jsonapiDoc(threeTerrs))
	k.ASC.Handle(http.MethodPost, "/v1/inAppPurchaseAvailabilities", http.StatusConflict,
		`{"errors":[{"status":"409","code":"STATE_ERROR","title":"cannot be removed",`+
			`"detail":"Only account holder can update or delete availability for in-app purchase with state (READY_FOR_SALE)."}]}`)
	k.Web.Handle(http.MethodPost, "/v1/inAppPurchaseAvailabilities", http.StatusCreated,
		`{"data":{"type":"inAppPurchaseAvailabilities","id":"av2"}}`)
	args := `{"product_id":"` + iapTestProduct + `","availability":{"remove_from_sale":true}`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`}`))
	if js := tooltest.JSON(t, res); !strings.Contains(js, "stops being sold in all 3 territories") {
		t.Fatalf("preview = %s", js)
	}
	k.Elicit(true)
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "iap_upsert"), k.Env, args+`,"confirm":true}`))
	wm := k.Web.Mutations()
	if len(wm) != 1 {
		t.Fatalf("web mutations = %v", requestLog(wm))
	}
	checkRemovalBody(t, wm[0], "inAppPurchase", "inAppPurchases/iap1")
	out := tooltest.Decode[iapUpsertOut](t, res)
	if a := out.Availability; a == nil || a.Note != "0 territories, via web session (Account Holder only)" {
		t.Fatalf("availability = %+v", out.Availability)
	}
}
