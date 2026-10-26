package monetization

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// prcSubCurrentFixture registers a subscription currently at USA 4.99 and
// DEU 12.99 (GBR unpriced); the 9.99 USA point equalizes to DEU 9.99 and
// GBR 8.99.
func prcSubCurrentFixture(k *tooltest.Kit) {
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"sub1"}]}}}]`,
		`{"type":"subscriptions","id":"sub1","attributes":{"productId":"se.jvcr.hjul.pro"}}`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/prices", http.StatusOK, jsonapiDoc(
		`[`+prcRow("subscriptionPrices", "sp-usa", "subscriptionPricePoint", "subscriptionPricePoints", "sUSA499", "USA", "2025-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-deu", "subscriptionPricePoint", "subscriptionPricePoints", "sDEU1299", "DEU", "2025-01-01", false)+`]`,
		prcPt("subscriptionPricePoints", "sUSA499", "USA", "4.99"), prcPt("subscriptionPricePoints", "sDEU1299", "DEU", "12.99")))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/pricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("subscriptionPricePoints", "sUSA999", "USA", "9.99")+`]`, prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("subscriptionPricePoints", "sDEU999", "DEU", "9.99")+`,`+prcPt("subscriptionPricePoints", "sGBR899", "GBR", "8.99")+`]`,
		prcTerr("DEU", "EUR"), prcTerr("GBR", "GBP")))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionPrices", http.StatusCreated, `{"data":{"type":"subscriptionPrices","id":"spN"}}`)
}

type prcDirResult struct {
	Changes   []prcChange `json:"changes"`
	Increases int         `json:"increases"`
	Decreases int         `json:"decreases"`
}

// Monetization#1: without preserve_current_price, existing subscribers keep
// their price; the plan marks each increase and the approval says so.
func TestPricingSubscriptionIncreaseDefaultsToPreserve(t *testing.T) {
	k := newKit(t)
	prcSubCurrentFixture(k)
	plan, res := prcPlanThenApply(t, k, `{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99"}`)
	tooltest.MustOK(t, res)
	dirs := map[string]string{}
	for _, c := range plan.Changes {
		dirs[c.Territory] = c.Direction
	}
	if dirs["USA"] != prcIncrease || dirs["DEU"] != prcDecrease || dirs["GBR"] != prcNew {
		t.Fatalf("directions = %v", dirs)
	}
	pv := k.Plans.Proposed[0]
	irr := strings.Join(pv.Irreversible, "\n")
	if !strings.Contains(pv.Summary, "1 increases") || !strings.Contains(irr, "1 territories increase: existing subscribers keep their current price") ||
		!strings.Contains(irr, "1 territories decrease") {
		t.Fatalf("preview = %+v", pv)
	}
	posts := k.ASC.RequestsTo(http.MethodPost, "/v1/subscriptionPrices")
	if len(posts) != 3 {
		t.Fatalf("posts = %d", len(posts))
	}
	for _, p := range posts {
		if a := reqAttrs(t, p); a["preserveCurrentPrice"] != true {
			t.Fatalf("attrs = %v", a)
		}
	}
}

// Monetization#1: opting out says what happens to existing subscribers.
func TestPricingSubscriptionIncreaseMovingSubscribersWarns(t *testing.T) {
	k := newKit(t)
	prcSubCurrentFixture(k)
	_, res := prcPlanThenApply(t, k, `{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99","preserve_current_price":false}`)
	tooltest.MustOK(t, res)
	irr := strings.Join(k.Plans.Proposed[0].Irreversible, "\n")
	if !strings.Contains(irr, "increase for existing subscribers too") || !strings.Contains(irr, "lapse") {
		t.Fatalf("irreversible = %s", irr)
	}
	for _, p := range k.ASC.RequestsTo(http.MethodPost, "/v1/subscriptionPrices") {
		if a := reqAttrs(t, p); a["preserveCurrentPrice"] != false {
			t.Fatalf("attrs = %v", a)
		}
	}
}

// Monetization#6: a territories filter without the base territory doesn't
// claim a price change there.
func TestPricingSubscriptionFilterWithoutBaseSummary(t *testing.T) {
	k := newKit(t)
	prcSubCurrentFixture(k)
	out := tooltest.Decode[prcDirResult](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_plan"), k.Env,
		`{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99","territories":["deu"]}`)))
	if len(out.Changes) != 1 || out.Changes[0].Territory != "DEU" {
		t.Fatalf("changes = %+v", out.Changes)
	}
	sum := k.Plans.Proposed[0].Summary
	if strings.Contains(sum, "in USA,") || !strings.Contains(sum, "USA itself is not changed") || !strings.Contains(sum, "only DEU") {
		t.Fatalf("summary = %s", sum)
	}
}

// Monetization#5: moving the base re-equalizes the old base instead of
// pinning it at its old manual price; the approval lists kept manual prices.
func TestPricingAppBaseMoveReequalizesOldBase(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPriceSchedule", http.StatusOK,
		`{"data":{"type":"appPriceSchedules","id":"sched1","relationships":{"baseTerritory":{"data":{"type":"territories","id":"USA"}}}},"included":[`+prcTerr("USA", "USD")+`]}`)
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/manualPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("mp1", "pUSA099", "USA", true)+`,`+prcAppRow("mp2", "pSWE10", "SWE", true)+`]`,
		prcPt("appPricePoints", "pUSA099", "USA", "0.99"), prcPt("appPricePoints", "pSWE10", "SWE", "10.00")))
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/automaticPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("ap1", "pGBR099", "GBR", false)+`]`, prcPt("appPricePoints", "pGBR099", "GBR", "0.99")))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pGBR499", "GBR", "4.99")+`]`, prcTerr("GBR", "GBP")))
	k.ASC.Handle(http.MethodGet, "/v3/appPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pUSA599", "USA", "5.99")+`,`+prcPt("appPricePoints", "pSWE55", "SWE", "55.00")+`]`,
		prcTerr("USA", "USD"), prcTerr("SWE", "SEK")))
	k.ASC.Handle(http.MethodPost, "/v1/appPriceSchedules", http.StatusCreated, `{"data":{"type":"appPriceSchedules","id":"sched2"}}`)

	plan, res := prcPlanThenApply(t, k, `{"base_territory":"GBR","price":"4.99"}`)
	tooltest.MustOK(t, res)
	var usa *prcChange
	for i, c := range plan.Changes {
		if c.Territory == "USA" {
			usa = &plan.Changes[i]
		}
	}
	if usa == nil || usa.New != "5.99" || usa.Source != "equalized" || strings.Join(plan.Kept, ",") != "SWE" {
		t.Fatalf("plan = %+v", plan)
	}
	sum := k.Plans.Proposed[0].Summary
	if !strings.Contains(sum, "base moves from USA") || !strings.Contains(sum, "1 territories keep manual prices: SWE") {
		t.Fatalf("summary = %s", sum)
	}
	post := k.ASC.Mutations()[0]
	var points []string
	for _, inc := range prcIncluded(t, post) {
		points = append(points, prcRelOf(inc, "appPricePoint"))
	}
	if reqRel(t, post, "baseTerritory") != "territories/GBR" ||
		strings.Join(points, ",") != "appPricePoints/pGBR499,appPricePoints/pSWE10" {
		t.Fatalf("base = %s manual = %v", reqRel(t, post, "baseTerritory"), points)
	}
}

// Monetization#5: reset_manual_prices drops manual prices back to
// equalization; with start_date they apply until then.
func TestPricingAppResetManualPrices(t *testing.T) {
	k := newKit(t)
	prcAppFixture(k)
	k.ASC.Handle(http.MethodPost, "/v1/appPriceSchedules", http.StatusCreated, `{"data":{"type":"appPriceSchedules","id":"sched2"}}`)
	plan, res := prcPlanThenApply(t, k, `{"price":"4.99","reset_manual_prices":true,"start_date":"2026-02-01"}`)
	tooltest.MustOK(t, res)
	if len(plan.Kept) != 0 {
		t.Fatalf("kept = %v", plan.Kept)
	}
	var swe *prcChange
	for i, c := range plan.Changes {
		if c.Territory == "SWE" {
			swe = &plan.Changes[i]
		}
	}
	if swe == nil || swe.New != "55.00" || swe.Source != "equalized" {
		t.Fatalf("changes = %+v", plan.Changes)
	}
	var rows []string
	for _, inc := range prcIncluded(t, k.ASC.Mutations()[0]) {
		a := inc["attributes"].(map[string]any)
		rows = append(rows, prcRelOf(inc, "appPricePoint")+" "+tooltest.JSON(t, a["startDate"])+"→"+tooltest.JSON(t, a["endDate"]))
	}
	equalLines(t, rows, []string{
		`appPricePoints/pUSA099 null→"2026-02-01"`,
		`appPricePoints/pUSA499 "2026-02-01"→null`,
		`appPricePoints/pSWE10 null→"2026-02-01"`,
	})
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "pricing_plan"), k.Env,
		`{"kind":"subscription","product_id":"x","price":"1","reset_manual_prices":true}`), "invalid_arguments")
}

// A schedule rewrite re-posts manual prices of untouched territories with
// their dates (a temporary SWE price keeps its end, a future DNK price
// survives) and names a future row it cannot carry (the base's own).
func TestPricingAppCarriesManualDatesAndNamesDropped(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPriceSchedule", http.StatusOK,
		`{"data":{"type":"appPriceSchedules","id":"sched1","relationships":{"baseTerritory":{"data":{"type":"territories","id":"USA"}}}},"included":[`+prcTerr("USA", "USD")+`]}`)
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/automaticPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("ap1", "pGBR099", "GBR", false)+`]`, prcPt("appPricePoints", "pGBR099", "GBR", "0.99"), prcTerr("GBR", "GBP")))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appPricePoints", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pUSA499", "USA", "4.99")+`]`, prcTerr("USA", "USD")))
	k.ASC.Handle(http.MethodGet, "/v3/appPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[`+prcPt("appPricePoints", "pGBR499", "GBR", "4.99")+`]`, prcTerr("GBR", "GBP")))
	swe := strings.Replace(prcAppRow("mp2", "pSWE10", "SWE", true), `"endDate":null`, `"endDate":"2099-11-30"`, 1)
	k.ASC.Handle(http.MethodGet, "/v1/appPriceSchedules/{id}/manualPrices", http.StatusOK, jsonapiDoc(
		`[`+prcAppRow("mp1", "pUSA099", "USA", true)+`,`+swe+`,`+
			prcRow("appPrices", "mp3", "appPricePoint", "appPricePoints", "pDNK70", "DNK", "2099-01-01", true)+`,`+
			prcRow("appPrices", "mp4", "appPricePoint", "appPricePoints", "pUSA599", "USA", "2099-06-01", true)+`]`,
		prcPt("appPricePoints", "pUSA099", "USA", "0.99"), prcPt("appPricePoints", "pSWE10", "SWE", "10.00"),
		prcPt("appPricePoints", "pDNK70", "DNK", "70.00"), prcPt("appPricePoints", "pUSA599", "USA", "5.99"),
		prcTerr("USA", "USD"), prcTerr("SWE", "SEK"), prcTerr("DNK", "DKK")))
	k.ASC.Handle(http.MethodPost, "/v1/appPriceSchedules", http.StatusCreated, `{"data":{"type":"appPriceSchedules","id":"sched2"}}`)
	_, res := prcPlanThenApply(t, k, `{"price":"4.99"}`)
	tooltest.MustOK(t, res)
	var rows []string
	for _, inc := range prcIncluded(t, k.ASC.Mutations()[0]) {
		a := inc["attributes"].(map[string]any)
		rows = append(rows, prcRelOf(inc, "appPricePoint")+" "+tooltest.JSON(t, a["startDate"])+"→"+tooltest.JSON(t, a["endDate"]))
	}
	equalLines(t, rows, []string{
		`appPricePoints/pUSA499 null→null`,
		`appPricePoints/pDNK70 "2099-01-01"→null`,
		`appPricePoints/pSWE10 null→"2099-11-30"`,
	})
	irr := strings.Join(k.Plans.Proposed[0].Irreversible, "\n")
	if !strings.Contains(irr, "USA 5.99 USD from 2099-06-01") || strings.Contains(irr, "DNK") {
		t.Fatalf("irreversible = %s", irr)
	}
}

// AgentUX#7a: products mode returns each product's price in every
// requested territory, and says where there is none.
func TestPricingGetProductsAllRequestedTerritories(t *testing.T) {
	k := newKit(t)
	prcIAPCatalog(k)
	out := tooltest.Decode[pricingGetProductsOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"iap","products":["6826637877","ivan.Sift.tip.big"],"territories":["USA","GBR","SWE"]}`)))
	for _, p := range out.Products {
		var got []string
		for _, r := range p.Prices {
			got = append(got, r.Territory+" "+r.Currency+" "+r.Price)
		}
		if strings.Join(got, ",") != "GBR GBP 0.99,USA USD 0.99" || strings.Join(p.Missing, ",") != "SWE" || p.Error != "" {
			t.Fatalf("product %s = %+v", p.ProductID, p)
		}
	}
	res := tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"iap","products":["6826637877"],"territories":["USA","GBR","SWE","DEU","FRA","ITA","ESP","NLD","CHE","NOR","DNK"]}`)
	tooltest.MustFail(t, res, "invalid_arguments")
}
