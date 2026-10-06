package monetization

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// ---- fixture builders ----

func prcTerr(id, cur string) string {
	return fmt.Sprintf(`{"type":"territories","id":%q,"attributes":{"currency":%q}}`, id, cur)
}

func prcPt(typ, id, terr, price string) string {
	return fmt.Sprintf(`{"type":%q,"id":%q,"attributes":{"customerPrice":%q,"proceeds":"1.00"},"relationships":{"territory":{"data":{"type":"territories","id":%q}}}}`,
		typ, id, price, terr)
}

func prcRow(typ, id, rel, relType, point, terr, start string, manual bool) string {
	s := "null"
	if start != "" {
		s = fmt.Sprintf("%q", start)
	}
	return fmt.Sprintf(`{"type":%q,"id":%q,"attributes":{"manual":%t,"startDate":%s,"endDate":null},"relationships":{%q:{"data":{"type":%q,"id":%q}},"territory":{"data":{"type":"territories","id":%q}}}}`,
		typ, id, manual, s, rel, relType, point, terr)
}

func prcAppRow(id, point, terr string, manual bool) string {
	return prcRow("appPrices", id, "appPricePoint", "appPricePoints", point, terr, "", manual)
}

// prcAppFixture registers the reads of an app whose base price is USA 0.99
// with a manual SWE override, plus price points and equalizations of 4.99.
func prcAppFixture(k *tooltest.Kit) {
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPriceSchedule", http.StatusOK,
		`{"data":{"type":"appPriceSchedules","id":"sched1","relationships":{"baseTerritory":{"data":{"type":"territories","id":"USA"}}}},"included":[`+prcTerr("USA", "USD")+`]}`)
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/manualPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("mp1", "pUSA099", "USA", true)+`,`+prcAppRow("mp2", "pSWE10", "SWE", true)+`]`,
		prcPt("appPricePoints", "pUSA099", "USA", "0.99"), prcPt("appPricePoints", "pSWE10", "SWE", "10.00"),
		prcTerr("USA", "USD"), prcTerr("SWE", "SEK")))
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/automaticPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("ap1", "pGBR099", "GBR", false)+`]`, prcPt("appPricePoints", "pGBR099", "GBR", "0.99"), prcTerr("GBR", "GBP")))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pUSA099", "USA", "0.99")+`,`+prcPt("appPricePoints", "pUSA499", "USA", "4.99")+`,`+
			prcPt("appPricePoints", "pUSA599", "USA", "5.99")+`]`, prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v3/appPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pGBR499", "GBR", "4.99")+`,`+prcPt("appPricePoints", "pSWE55", "SWE", "55.00")+`]`,
		prcTerr("GBR", "GBP"), prcTerr("SWE", "SEK")))
}

type prcPlanResult struct {
	Status   string      `json:"status"`
	PlanID   string      `json:"plan_id"`
	Changes  []prcChange `json:"changes"`
	Kept     []string    `json:"kept_manual"`
	Calls    int         `json:"calls"`
	CallPlan []prcCall   `json:"call_plan"`
	Blockers []string    `json:"blockers"`
	Avail    *struct {
		Create  bool             `json:"create"`
		Changes []prcAvailChange `json:"changes"`
	} `json:"availability"`
}

// prcPlanThenApply runs pricing_plan and then pricing_apply with the plan id.
func prcPlanThenApply(t *testing.T, k *tooltest.Kit, args string) (prcPlanResult, *tool.Result) {
	t.Helper()
	before := len(k.ASC.Mutations())
	plan := tooltest.Decode[prcPlanResult](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_plan"), k.Env, args)))
	if plan.Status != "planned" || plan.PlanID == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(k.ASC.Mutations()) != before {
		t.Fatal("pricing_plan mutated")
	}
	confirmed := strings.TrimSuffix(strings.TrimSpace(args), "}") + `,"plan_id":"` + plan.PlanID + `"}`
	k.Elicit(true) // pricing_apply requires a human accept
	return plan, tooltest.Call(t, mustTool(t, "pricing_apply"), k.Env, confirmed)
}

// prcIncluded returns the included array of a request body.
func prcIncluded(t *testing.T, r tool.Request) []map[string]any {
	t.Helper()
	raw, _ := bodyOf(t, r)["included"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

func prcRelOf(m map[string]any, name string) string {
	d, _ := m["relationships"].(map[string]any)[name].(map[string]any)["data"].(map[string]any)
	if d == nil {
		return ""
	}
	return d["type"].(string) + "/" + d["id"].(string)
}

// ---- tests ----

func TestPricingGetApp(t *testing.T) {
	k := newKit(t)
	prcAppFixture(k)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appAvailabilityV2", http.StatusOK,
		`{"data":{"type":"appAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
	k.ASC.Handle(http.MethodGet, "/v2/appAvailabilities/{id}/territoryAvailabilities", http.StatusOK, jsonapiDoc(`[`+
		prcTADoc("ta-usa", "USA", true)+`,`+prcTADoc("ta-gbr", "GBR", true)+`,`+prcTADoc("ta-swe", "SWE", false)+`]`))
	out := tooltest.Decode[pricingGetOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env, `{}`)))
	if out.BaseTerritory != "USA" || out.TerritoryCount != 3 || len(out.Prices) != 3 || out.Prices[0].Territory != "USA" {
		t.Fatalf("out = %+v", out)
	}
	if out.Prices[0].Price != "0.99" || out.Prices[0].Currency != "USD" || !out.Prices[0].Manual || out.Prices[0].PointID != "" {
		t.Fatalf("USA = %+v", out.Prices[0])
	}
	if a := out.Availability; a == nil || a.AvailableTerritories != 2 || a.UnavailableTerritories != 1 || !a.AvailableInNewTerritories {
		t.Fatalf("availability = %+v", out.Availability)
	}
}

// prcIAPCatalog registers two IAPs and their IAP schedules (base USA).
func prcIAPCatalog(k *tooltest.Kit) {
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, jsonapiDoc(
		`[{"type":"inAppPurchases","id":"6826637877","attributes":{"productId":"ivan.Sift.tip.bulle"}},
		  {"type":"inAppPurchases","id":"6826637878","attributes":{"productId":"ivan.Sift.tip.big"}}]`))
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/iapPriceSchedule", http.StatusOK,
		`{"data":{"type":"inAppPurchasePriceSchedules","id":"is1","relationships":{"baseTerritory":{"data":{"type":"territories","id":"USA"}}}},"included":[`+prcTerr("USA", "USD")+`]}`)
	k.ASC.Handle(http.MethodGet, "/v1/inAppPurchasePriceSchedules/{id}/manualPrices", http.StatusOK, jsonapiDoc(
		`[`+prcRow("inAppPurchasePrices", "m1", "inAppPurchasePricePoint", "inAppPurchasePricePoints", "iUSA099", "USA", "", true)+`]`,
		prcPt("inAppPurchasePricePoints", "iUSA099", "USA", "0.99"), prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v1/inAppPurchasePriceSchedules/{id}/automaticPrices", http.StatusOK, jsonapiDoc(
		`[`+prcRow("inAppPurchasePrices", "a1", "inAppPurchasePricePoint", "inAppPurchasePricePoints", "iGBR099", "GBR", "", false)+`]`,
		prcPt("inAppPurchasePricePoints", "iGBR099", "GBR", "0.99"), prcTerr("GBR", "GBP")))
}

func TestPricingGetIAPByResourceID(t *testing.T) {
	k := newKit(t)
	prcIAPCatalog(k)
	out := tooltest.Decode[pricingGetOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"iap","product_id":"6826637877","for_plan":true}`)))
	if out.Target.ID != "6826637877" || out.Target.ProductID != "ivan.Sift.tip.bulle" || out.TerritoryCount != 2 {
		t.Fatalf("out = %+v", out)
	}
	if out.Prices[0].Territory != "USA" || out.Prices[0].PointID != "iUSA099" || out.Note != "" {
		t.Fatalf("prices = %+v", out.Prices)
	}
}

func TestPricingGetProducts(t *testing.T) {
	k := newKit(t)
	prcIAPCatalog(k)
	out := tooltest.Decode[pricingGetProductsOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"iap","products":["6826637877","ivan.Sift.tip.big","nope"]}`)))
	if len(out.Products) != 3 {
		t.Fatalf("out = %+v", out)
	}
	for i, want := range []string{"ivan.Sift.tip.bulle", "ivan.Sift.tip.big"} {
		p := out.Products[i]
		if p.ProductID != want || p.Territory != "USA" || p.Price != "0.99" || p.Currency != "USD" || p.TerritoryCount != 2 || p.Error != "" {
			t.Fatalf("product %d = %+v", i, p)
		}
	}
	if out.Products[2].Error == "" || out.Products[2].Ref != "nope" {
		t.Fatalf("unknown = %+v", out.Products[2])
	}
	if n := strings.Count(strings.Join(requestLog(k.ASC.Requests()), "\n"), "inAppPurchasesV2"); n != 1 {
		t.Fatalf("catalog listed %d times", n)
	}
	res := tooltest.Call(t, mustTool(t, "pricing_get"), k.Env, `{"kind":"iap","products":["a","b","c","d","e","f","g","h","i","j","k"]}`)
	if !res.IsError {
		t.Fatal("11 products accepted")
	}
}

func TestPricingAppPlanApplySinglePost(t *testing.T) {
	k := newKit(t)
	prcAppFixture(k)
	k.ASC.Handle(http.MethodPost, "/v1/appPriceSchedules", http.StatusCreated, `{"data":{"type":"appPriceSchedules","id":"sched2"}}`)
	plan, res := prcPlanThenApply(t, k, `{"price":"4.99"}`)
	if plan.Calls != 1 || len(plan.Changes) != 2 || plan.Changes[0].Territory != "USA" || plan.Changes[0].Old != "0.99" ||
		plan.Changes[0].New != "4.99" || plan.Changes[1].Territory != "GBR" || plan.Changes[1].Source != "equalized" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Kept) != 1 || plan.Kept[0] != "SWE" {
		t.Fatalf("kept = %v", plan.Kept)
	}
	tooltest.MustOK(t, res)
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Path != "/v1/appPriceSchedules" {
		t.Fatalf("mutations = %v", requestLog(muts))
	}
	post := muts[0]
	if reqRel(t, post, "app") != "apps/app1" || reqRel(t, post, "baseTerritory") != "territories/USA" {
		t.Fatalf("rels = %v", bodyOf(t, post))
	}
	inc := prcIncluded(t, post)
	if len(inc) != 2 || inc[0]["type"] != "appPrices" || prcRelOf(inc[0], "appPricePoint") != "appPricePoints/pUSA499" ||
		prcRelOf(inc[1], "appPricePoint") != "appPricePoints/pSWE10" {
		t.Fatalf("included = %v", inc)
	}
	mp := bodyOf(t, post)["data"].(map[string]any)["relationships"].(map[string]any)["manualPrices"].(map[string]any)["data"].([]any)
	if len(mp) != 2 || mp[0].(map[string]any)["id"] != inc[0]["id"] {
		t.Fatalf("manualPrices = %v", mp)
	}
}

func TestPricingIAPSchedule(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, iapLookup, http.StatusOK, iapProductDoc("iap1", "Coins", "", false))
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/iapPriceSchedule", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	k.ASC.Handle(http.MethodGet, "/v2/inAppPurchases/{id}/pricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("inAppPurchasePricePoints", "iUSA199", "USA", "1.99")+`,`+prcPt("inAppPurchasePricePoints", "iUSA299", "USA", "2.99")+`]`,
		prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v1/inAppPurchasePricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("inAppPurchasePricePoints", "iGBR199", "GBR", "1.99")+`]`, prcTerr("GBR", "GBP")))
	k.ASC.Handle(http.MethodPost, "/v1/inAppPurchasePriceSchedules", http.StatusCreated, `{"data":{"type":"inAppPurchasePriceSchedules","id":"is1"}}`)
	plan, res := prcPlanThenApply(t, k, `{"kind":"iap","product_id":"`+iapTestProduct+`","price":"2.00"}`)
	if len(plan.Changes) != 2 || plan.Changes[0].PointID != "iUSA199" {
		t.Fatalf("plan = %+v (nearest of 2.00 must be 1.99)", plan)
	}
	tooltest.MustOK(t, res)
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Path != "/v1/inAppPurchasePriceSchedules" || reqRel(t, muts[0], "inAppPurchase") != "inAppPurchases/iap1" {
		t.Fatalf("mutations = %v", requestLog(muts))
	}
	inc := prcIncluded(t, muts[0])
	if len(inc) != 1 || prcRelOf(inc[0], "inAppPurchasePricePoint") != "inAppPurchasePricePoints/iUSA199" ||
		prcRelOf(inc[0], "inAppPurchaseV2") != "inAppPurchases/iap1" {
		t.Fatalf("included = %v", inc)
	}
}

// prcSubFake serves subscription prices from the POSTs that succeeded; the
// POST numbered failAt fails with a 500 (not applied).
type prcSubFake struct {
	mu      sync.Mutex
	applied map[string]string
	posts   []string
	failAt  int
}

func (f *prcSubFake) register(t *testing.T, k *tooltest.Kit) {
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"sub1"}]}}}]`,
		`{"type":"subscriptions","id":"sub1","attributes":{"productId":"se.jvcr.hjul.pro"}}`))
	k.ASC.HandleFunc(http.MethodGet, "/v1/subscriptions/{id}/prices", func(tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		rows, inc := []string{}, []string{}
		for terr, pt := range f.applied {
			rows = append(rows, prcRow("subscriptionPrices", "sp-"+terr, "subscriptionPricePoint", "subscriptionPricePoints", pt, terr, "2026-01-01", false))
			inc = append(inc, prcPt("subscriptionPricePoints", pt, terr, "9.99"))
		}
		return tooltest.Fixture{Body: []byte(jsonapiDoc("["+strings.Join(rows, ",")+"]", inc...))}
	})
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/pricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("subscriptionPricePoints", "sUSA999", "USA", "9.99")+`]`, prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("subscriptionPricePoints", "sDEU999", "DEU", "9.99")+`,`+prcPt("subscriptionPricePoints", "sGBR899", "GBR", "8.99")+`]`,
		prcTerr("DEU", "EUR"), prcTerr("GBR", "GBP")))
	k.ASC.HandleFunc(http.MethodPost, "/v1/subscriptionPrices", func(r tool.Request) tooltest.Fixture {
		b, _ := json.Marshal(r.Body)
		var body struct {
			Data struct {
				Relationships map[string]struct {
					Data struct{ ID string } `json:"data"`
				} `json:"relationships"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &body); err != nil {
			t.Error(err)
		}
		terr, pt := body.Data.Relationships["territory"].Data.ID, body.Data.Relationships["subscriptionPricePoint"].Data.ID
		f.mu.Lock()
		defer f.mu.Unlock()
		f.posts = append(f.posts, terr)
		if len(f.posts) == f.failAt {
			return tooltest.Fixture{Status: http.StatusInternalServerError, Body: []byte(`{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"boom"}]}`)}
		}
		f.applied[terr] = pt
		return tooltest.Fixture{Status: http.StatusCreated, Body: []byte(`{"data":{"type":"subscriptionPrices","id":"sp-` + terr + `"}}`)}
	})
}

func TestPricingSubscriptionResumeSkipsApplied(t *testing.T) {
	k := newKit(t)
	f := &prcSubFake{applied: map[string]string{}, failAt: 2}
	f.register(t, k)
	args := `{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99","preserve_current_price":true}`

	plan, res := prcPlanThenApply(t, k, args)
	if plan.Calls != 3 || len(plan.CallPlan) != 1 || plan.CallPlan[0].Count != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	if !res.IsError {
		t.Fatalf("apply should be interrupted: %+v", res.Data)
	}
	body := tooltest.Decode[struct {
		Error    string          `json:"error"`
		Progress pricingApplyOut `json:"progress"`
		Next     []tool.NextStep `json:"next"`
	}](t, res)
	if body.Error != "outcome_unknown" || strings.Join(body.Progress.Applied, ",") != "USA" ||
		strings.Join(body.Progress.Pending, ",") != "DEU,GBR" {
		t.Fatalf("interrupted = %+v", body)
	}
	if len(res.Next) == 0 || res.Next[len(res.Next)-1].Tool != "pricing_plan" {
		t.Fatalf("next = %+v", res.Next)
	}
	sub := k.ASC.RequestsTo(http.MethodPost, "/v1/subscriptionPrices")[0]
	if a := reqAttrs(t, sub); a["preserveCurrentPrice"] != true || reqRel(t, sub, "subscription") != "subscriptions/sub1" {
		t.Fatalf("first post = %v", bodyOf(t, sub))
	}

	// Re-diff: USA is already applied and must not be POSTed again.
	plan2, res2 := prcPlanThenApply(t, k, args)
	if plan2.Calls != 2 || plan2.Changes[0].Territory != "DEU" || plan2.Changes[1].Territory != "GBR" {
		t.Fatalf("replan = %+v", plan2)
	}
	out := tooltest.Decode[pricingApplyOut](t, tooltest.MustOK(t, res2))
	if strings.Join(out.Applied, ",") != "DEU,GBR" {
		t.Fatalf("applied = %v", out.Applied)
	}
	if got := strings.Join(f.posts, ","); got != "USA,DEU,DEU,GBR" {
		t.Fatalf("posts = %s", got)
	}
}

func TestPricingRateLimitBlocks(t *testing.T) {
	k := newKit(t)
	f := &prcSubFake{applied: map[string]string{}}
	f.register(t, k)
	k.ASC.Limit = tool.RateLimit{Limit: 3600, Remaining: 202, Known: true}
	out := tooltest.Decode[prcPlanResult](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_plan"), k.Env,
		`{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99"}`)))
	if out.Status != "blocked" || out.PlanID != "" || len(out.Blockers) != 1 || len(k.Plans.Proposed) != 0 {
		t.Fatalf("out = %+v", out)
	}
	res := tooltest.Call(t, mustTool(t, "pricing_apply"), k.Env, `{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99","plan_id":"x"}`)
	tooltest.MustFail(t, res, "rate_limited")
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("mutated despite rate limit")
	}
}

func prcTADoc(id, terr string, avail bool) string {
	return fmt.Sprintf(`{"type":"territoryAvailabilities","id":%q,"attributes":{"available":%t},"relationships":{"territory":{"data":{"type":"territories","id":%q}}}}`,
		id, avail, terr)
}

func TestPricingAvailabilityExistingPatches(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appAvailabilityV2", http.StatusOK,
		`{"data":{"type":"appAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
	k.ASC.Handle(http.MethodGet, "/v2/appAvailabilities/{id}/territoryAvailabilities", http.StatusOK, jsonapiDoc(`[`+
		prcTADoc("ta-usa", "USA", true)+`,`+prcTADoc("ta-gbr", "GBR", true)+`,`+prcTADoc("ta-swe", "SWE", false)+`]`))
	k.ASC.Handle(http.MethodPatch, "/v1/territoryAvailabilities/{id}", http.StatusOK, `{"data":{"type":"territoryAvailabilities","id":"x"}}`)
	plan, res := prcPlanThenApply(t, k, `{"availability":{"add":["swe","USA"],"remove":["GBR"]}}`)
	if plan.Calls != 2 || plan.Avail == nil || plan.Avail.Create || len(plan.Avail.Changes) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	out := tooltest.Decode[pricingApplyOut](t, tooltest.MustOK(t, res))
	if out.AvailabilityCalls != 2 {
		t.Fatalf("out = %+v", out)
	}
	equalLines(t, requestLog(k.ASC.Mutations()), []string{
		"PATCH /v1/territoryAvailabilities/ta-gbr",
		"PATCH /v1/territoryAvailabilities/ta-swe",
	})
	if a := reqAttrs(t, k.ASC.Mutations()[0]); a["available"] != false {
		t.Fatalf("GBR attrs = %v", a)
	}
	if a := reqAttrs(t, k.ASC.Mutations()[1]); a["available"] != true {
		t.Fatalf("SWE attrs = %v", a)
	}
}

func TestPricingAvailabilityMissingSinglePost(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appAvailabilityV2", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/territories", http.StatusOK, jsonapiDoc(`[`+prcTerr("USA", "USD")+`,`+prcTerr("GBR", "GBP")+`,`+prcTerr("SWE", "SEK")+`]`))
	k.ASC.Handle(http.MethodPost, "/v2/appAvailabilities", http.StatusCreated, `{"data":{"type":"appAvailabilities","id":"av1"}}`)
	plan, res := prcPlanThenApply(t, k, `{"availability":{"remove":["GBR"]}}`)
	if plan.Calls != 1 || plan.Avail == nil || !plan.Avail.Create {
		t.Fatalf("plan = %+v", plan)
	}
	tooltest.MustOK(t, res)
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Path != "/v2/appAvailabilities" || reqRel(t, muts[0], "app") != "apps/app1" {
		t.Fatalf("mutations = %v", requestLog(muts))
	}
	if a := reqAttrs(t, muts[0]); a["availableInNewTerritories"] != true {
		t.Fatalf("attrs = %v", a)
	}
	got := map[string]any{}
	for _, inc := range prcIncluded(t, muts[0]) {
		got[strings.TrimPrefix(prcRelOf(inc, "territory"), "territories/")] = inc["attributes"].(map[string]any)["available"]
	}
	if len(got) != 3 || got["GBR"] != false || got["USA"] != true || got["SWE"] != true {
		t.Fatalf("territories = %v", got)
	}
}

func TestPricingStalePlanRefused(t *testing.T) {
	k := newKit(t)
	prcAppFixture(k)
	// The second read (during apply) shows a price changed since planning.
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/manualPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("mp1", "pUSA599", "USA", true)+`]`, prcPt("appPricePoints", "pUSA599", "USA", "5.99"), prcTerr("USA", "USD")))
	_, res := prcPlanThenApply(t, k, `{"price":"4.99"}`)
	tooltest.MustFail(t, res, "stale_plan")
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("stale plan mutated")
	}
}

func TestPricingPlanValidation(t *testing.T) {
	k := newKit(t)
	for _, args := range []string{
		`{}`,
		`{"overrides":{"SWE":"10"}}`,
		`{"price":"1","plan_type":"MONTHLY"}`,
		`{"kind":"iap","product_id":"x.y","availability":{"add":["USA"]}}`,
		`{"price":"1","start_date":"2020-01-01"}`,
		`{"availability":{"add":["US"]}}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "pricing_plan"), k.Env, args), "invalid_arguments")
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatalf("requests before validation: %v", requestLog(k.ASC.Requests()))
	}
}
