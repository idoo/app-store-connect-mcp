package monetization

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// subPt is a subscription price point shaped like Apple's live response
// (Sikt ivan.Sift.fika.monthly, 2026-10-26): raw strings such as "1.4",
// "29.0" and a proceedsYear2 attribute.
func subPt(id, terr, price, proceeds, year2 string) string {
	return fmt.Sprintf(`{"type":"subscriptionPricePoints","id":%q,"attributes":{"customerPrice":%q,"proceeds":%q,"proceedsYear2":%q},`+
		`"relationships":{"territory":{"data":{"type":"territories","id":%q}}}}`, id, price, proceeds, year2, terr)
}

// MA4 + MA5: subscription rows show year-2 proceeds, and every amount is
// formatted with its currency's decimals (USD 2, JPY 0, SEK 2). Values and the
// USD-priced KWT storefront match the live response; KWD 3 decimals: TestPrcMoney.
func TestPricingGetSubscriptionYear2AndCurrencyDecimals(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"sub1"}]}}}]`,
		`{"type":"subscriptions","id":"sub1","attributes":{"productId":"ivan.Sift.fika.monthly"}}`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/prices", http.StatusOK, jsonapiDoc(
		`[`+prcRow("subscriptionPrices", "sp-usa", "subscriptionPricePoint", "subscriptionPricePoints", "pUSA", "USA", "2025-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-jpn", "subscriptionPricePoint", "subscriptionPricePoints", "pJPN", "JPN", "2025-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-swe", "subscriptionPricePoint", "subscriptionPricePoints", "pSWE", "SWE", "2025-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-kwt", "subscriptionPricePoint", "subscriptionPricePoints", "pKWT", "KWT", "2025-01-01", false)+`]`,
		subPt("pUSA", "USA", "1.99", "1.4", "1.69"), subPt("pJPN", "JPN", "300.0", "191", "232"),
		subPt("pSWE", "SWE", "29.0", "16.24", "19.72"), subPt("pKWT", "KWT", "1.99", "1.4", "1.69"),
		prcTerr("USA", "USD"), prcTerr("JPN", "JPY"), prcTerr("SWE", "SEK"), prcTerr("KWT", "USD")))

	out := tooltest.Decode[pricingGetOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"subscription","product_id":"ivan.Sift.fika.monthly","territories":["USA","JPN","SWE","KWT"]}`)))
	type money struct{ price, proceeds, year2 string }
	want := map[string]money{
		"USA": {"1.99", "1.40", "1.69"},
		"JPN": {"300", "191", "232"},
		"SWE": {"29.00", "16.24", "19.72"},
		"KWT": {"1.99", "1.40", "1.69"},
	}
	if len(out.Prices) != len(want) {
		t.Fatalf("prices = %+v", out.Prices)
	}
	for _, r := range out.Prices {
		got := money{r.Price, r.Proceeds, r.Year2}
		if w := want[r.Territory]; got != w {
			t.Errorf("%s: got %+v, want %+v", r.Territory, got, w)
		}
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/subscriptions/sub1/prices")
	if len(reqs) == 0 || !strings.Contains(reqs[0].Query.Get("fields[subscriptionPricePoints]"), "proceedsYear2") {
		t.Fatalf("prices request does not ask for proceedsYear2: %+v", reqs)
	}
}

// MA5: Apple's raw price strings get the currency's decimals, like reports.
func TestPrcMoney(t *testing.T) {
	for _, tc := range []struct{ cur, in, want string }{
		{"USD", "1.4", "1.40"},
		{"JPY", "300.0", "300"},
		{"SEK", "29.0", "29.00"},
		{"KWD", "1.2", "1.200"},
		{"HUF", "1290.5", "1290.50"},
		{"JPY", "255.5", "255.5"},     // B6: zero-fill only, never round away digits
		{"KRW", "1234.56", "1234.56"}, // B6
		{"USD", "0.005", "0.005"},     // B6
		{"USD", "0x10", "0x10"},       // B7: hex is not a decimal
		{"", "1.4", "1.4"},            // unknown currency: as Apple sent it
		{"USD", "", ""},               // absent stays absent
		{"USD", "n/a", "n/a"},         // not a decimal: as Apple sent it
	} {
		if got := prcMoney(tc.cur, tc.in); got != tc.want {
			t.Errorf("prcMoney(%q, %q) = %q, want %q", tc.cur, tc.in, got, tc.want)
		}
	}
}

// L2-1: products mode shows each territory's next scheduled price and its
// start date, and points at pricing_get product_id for the full schedule.
func TestPricingGetProductsShowsNextScheduledPrice(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"sub1"}]}}}]`,
		`{"type":"subscriptions","id":"sub1","attributes":{"productId":"ivan.Sift.fika.annual"}}`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/prices", http.StatusOK, jsonapiDoc(
		`[`+prcRow("subscriptionPrices", "sp-usa", "subscriptionPricePoint", "subscriptionPricePoints", "pUSA", "USA", "2025-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-usa2", "subscriptionPricePoint", "subscriptionPricePoints", "pUSA2", "USA", "2099-02-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-usa3", "subscriptionPricePoint", "subscriptionPricePoints", "pUSA3", "USA", "2099-01-01", false)+`,`+
			prcRow("subscriptionPrices", "sp-swe", "subscriptionPricePoint", "subscriptionPricePoints", "pSWE", "SWE", "2025-01-01", false)+`]`,
		subPt("pUSA", "USA", "24.99", "17", "21"), subPt("pUSA2", "USA", "34.99", "24", "29"),
		subPt("pUSA3", "USA", "29.99", "21", "25"), subPt("pSWE", "SWE", "299.0", "190", "230"),
		prcTerr("USA", "USD"), prcTerr("SWE", "SEK")))

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"subscription","products":["ivan.Sift.fika.annual"],"territories":["USA","SWE"]}`))
	out := tooltest.Decode[pricingGetProductsOut](t, res)
	p := out.Products[0]
	if p.Scheduled != 2 || len(p.Prices) != 2 {
		t.Fatalf("product = %+v", p)
	}
	byTerr := map[string]prcProductRow{}
	for _, r := range p.Prices {
		byTerr[r.Territory] = r
	}
	usa, swe := byTerr["USA"], byTerr["SWE"]
	if usa.Price != "24.99" || usa.NextPrice == nil || *usa.NextPrice != (prcNextPrice{Price: "29.99", StartDate: "2099-01-01"}) {
		t.Errorf("USA = %+v next %+v", usa, usa.NextPrice)
	}
	if swe.NextPrice != nil {
		t.Errorf("SWE has no scheduled change, got %+v", swe.NextPrice)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "pricing_get" || res.Next[0].Args["product_id"] != "sub1" {
		t.Errorf("next = %+v", res.Next)
	}

	// Base-territory summary carries next_price too.
	out = tooltest.Decode[pricingGetProductsOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "pricing_get"), k.Env,
		`{"kind":"subscription","products":["sub1"]}`)))
	if b := out.Products[0]; b.Territory != "USA" || b.NextPrice == nil || b.NextPrice.Price != "29.99" {
		t.Errorf("base = %+v next %+v", b, b.NextPrice)
	}
}
