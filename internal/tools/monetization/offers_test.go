package monetization

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool/tooltest"
)

const (
	offerTestProduct = "se.jvcr.hjul.plus.year"
	offerGroupsPath  = "/v1/apps/{id}/subscriptionGroups"
)

// offerKit is newKit with the app's single subscription s1 registered.
func offerKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, offerGroupsPath, http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Plus"},
		  "relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"s1"}]}}}]`,
		`{"type":"subscriptions","id":"s1","attributes":{"productId":"`+offerTestProduct+`","name":"Yearly"}}`))
	return k
}

// offerMutationBodies returns "METHOD path" plus decoded body of each mutation.
func offerMutationBodies(t *testing.T, k *tooltest.Kit) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range k.ASC.Mutations() {
		out = append(out, map[string]any{"request": r.Method + " " + r.Path, "body": bodyOf(t, r)})
	}
	return out
}

func TestOffersListOneSubscription(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/introductoryOffers", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionIntroductoryOffers","id":"io1","attributes":{"offerMode":"FREE_TRIAL","duration":"ONE_WEEK","numberOfPeriods":1},
		   "relationships":{"territory":{"data":{"type":"territories","id":"USA"}},"subscriptionPricePoint":{"data":null}}},
		  {"type":"subscriptionIntroductoryOffers","id":"io2","attributes":{"offerMode":"FREE_TRIAL","duration":"ONE_WEEK","numberOfPeriods":1},
		   "relationships":{"territory":{"data":{"type":"territories","id":"GBR"}}}},
		  {"type":"subscriptionIntroductoryOffers","id":"io3","attributes":{"offerMode":"PAY_AS_YOU_GO","duration":"ONE_MONTH","numberOfPeriods":3},
		   "relationships":{"territory":{"data":{"type":"territories","id":"SWE"}},"subscriptionPricePoint":{"data":{"type":"subscriptionPricePoints","id":"ppSWE"}}}}]`,
		`{"type":"subscriptionPricePoints","id":"ppSWE","attributes":{"customerPrice":"19.0"}}`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/promotionalOffers", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionPromotionalOffers","id":"po1","attributes":{"name":"Winter","offerCode":"winter","offerMode":"PAY_UP_FRONT","duration":"THREE_MONTHS","numberOfPeriods":1},
		   "relationships":{"prices":{"data":[{"type":"subscriptionPromotionalOfferPrices","id":"p1"},{"type":"subscriptionPromotionalOfferPrices","id":"p2"}]}}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/winBackOffers", http.StatusOK, jsonapiDoc(
		`[{"type":"winBackOffers","id":"wb1","attributes":{"referenceName":"Come back","offerId":"comeback","offerMode":"PAY_AS_YOU_GO","duration":"ONE_MONTH","periodCount":2,
		   "customerEligibilityPaidSubscriptionDurationInMonths":3,"customerEligibilityTimeSinceLastSubscribedInMonths":{"minimum":1,"maximum":6},"priority":"HIGH","startDate":"2026-11-01"},
		   "relationships":{"prices":{"data":[{"type":"winBackOfferPrices","id":"w1"}]}}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/offerCodes", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionOfferCodes","id":"oc1","attributes":{"name":"Spring","customerEligibilities":["NEW"],"offerEligibility":"STACK_WITH_INTRO_OFFERS",
		   "offerMode":"FREE_TRIAL","duration":"ONE_MONTH","numberOfPeriods":1,"totalNumberOfCodes":110,"productionCodeCount":100,"sandboxCodeCount":10,"active":true},
		   "relationships":{"oneTimeUseCodes":{"data":[{"type":"subscriptionOfferCodeOneTimeUseCodes","id":"ot1"}]},
		    "customCodes":{"data":[{"type":"subscriptionOfferCodeCustomCodes","id":"cc1"}]},"prices":{"data":[]}}}]`,
		`{"type":"subscriptionOfferCodeCustomCodes","id":"cc1","attributes":{"customCode":"HJULSPRING","numberOfCodes":10,"active":true}}`))

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_list"), k.Env, `{"product_id":"`+offerTestProduct+`"}`))
	equalLines(t, requestLog(k.ASC.Requests()), []string{
		"GET /v1/apps/app1/subscriptionGroups",
		"GET /v1/subscriptions/s1/introductoryOffers",
		"GET /v1/subscriptions/s1/promotionalOffers",
		"GET /v1/subscriptions/s1/winBackOffers",
		"GET /v1/subscriptions/s1/offerCodes",
	})
	tooltest.AssertGolden(t, "testdata/offers/offers_list.json", res)
	out := tooltest.Decode[offersListOut](t, res)
	s := out.Subscriptions[0]
	if s.Counts[offerKindIntro] != 3 || s.Counts[offerKindPromo] != 1 || s.Counts[offerKindWinBack] != 1 || s.Counts[offerKindCode] != 1 {
		t.Fatalf("counts = %v", s.Counts)
	}
	trial := s.Offers[0]
	if trial.Territories != 2 || len(trial.IDs) != 2 || trial.Mode != "FREE_TRIAL" {
		t.Fatalf("grouped trial = %+v", trial)
	}
	code := s.Offers[len(s.Offers)-1]
	if code.Codes == nil || code.Codes.Total != 110 || code.Codes.OneTimeBatches != 1 || len(code.Codes.CustomCodes) != 1 {
		t.Fatalf("offer code = %+v", code)
	}
}

func TestOffersCreateIntroPerTerritory(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/pricePoints", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionPricePoints","id":"ppUSA049","attributes":{"customerPrice":"0.49"}},
		  {"type":"subscriptionPricePoints","id":"ppUSA099","attributes":{"customerPrice":"0.99"}}]`))
	// GBR already has an intro offer: it is skipped, never re-created.
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/introductoryOffers", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionIntroductoryOffers","id":"io1","attributes":{"offerMode":"PAY_AS_YOU_GO","duration":"ONE_MONTH","numberOfPeriods":3},
		   "relationships":{"territory":{"data":{"type":"territories","id":"GBR"}}}}]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionIntroductoryOffers", http.StatusCreated,
		`{"data":{"type":"subscriptionIntroductoryOffers","id":"ioN","attributes":{"offerMode":"PAY_AS_YOU_GO"}}}`)

	args := `{"product_id":"` + offerTestProduct + `",
		"kind":"intro","mode":"PAY_AS_YOU_GO","duration":"ONE_MONTH","periods":3,"start_date":"2026-11-01",
		"prices":[{"territory":"usa","price":"0.99"},{"territory":"GBR","price_point_id":"ppGBR"}]`
	// Preview: terms and territory prices, nothing created.
	prev := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, args+`}`))
	if pv := tooltest.JSON(t, prev.Data); !strings.Contains(pv, `"needs_confirm"`) || !strings.Contains(pv, `"USA":"0.99"`) ||
		!strings.Contains(pv, "PAY_AS_YOU_GO") || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %s", pv)
	}
	// RequireHuman: the agent's confirm alone is refused.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, args+`,"confirm":true}`), "human_approval_required")
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, args+`,"confirm":true}`))
	if got := k.ASC.RequestsTo(http.MethodGet, "/v1/subscriptions/{id}/pricePoints"); len(got) != 3 || got[0].Query.Get("filter[territory]") != "USA" {
		t.Fatalf("price point lookup = %+v", got)
	}
	tooltest.AssertGolden(t, "testdata/offers/intro_create_requests.json", offerMutationBodies(t, k))
	out := tooltest.Decode[offersCreateOut](t, res)
	if out.Created != 1 || len(out.Skipped) != 1 || out.Skipped[0] != "GBR" || out.Territories != 2 {
		t.Fatalf("out = %+v", out)
	}
}

func TestOffersCreatePromoRepeatIsAlready(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/promotionalOffers", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionPromotionalOffers","id":"po1","attributes":{"name":"Winter","offerCode":"winter","offerMode":"PAY_UP_FRONT","duration":"THREE_MONTHS","numberOfPeriods":1},
		   "relationships":{"prices":{"data":[{"type":"subscriptionPromotionalOfferPrices","id":"p1"}]}}}]`))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, `{"product_id":"`+offerTestProduct+`",
		"kind":"promotional","name":"Winter","identifier":"winter","mode":"PAY_UP_FRONT","duration":"THREE_MONTHS",
		"prices":[{"territory":"USA","price_point_id":"ppUSA"}]}`))
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("repeat must not write: %+v", m)
	}
	got := tooltest.Decode[map[string]any](t, res)
	if got["already"] != true || got["offer"].(map[string]any)["id"] != "po1" {
		t.Fatalf("result = %v", got)
	}
}

func TestOffersCreatePromoBody(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/promotionalOffers", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionPromotionalOffers", http.StatusCreated,
		`{"data":{"type":"subscriptionPromotionalOffers","id":"poN","attributes":{"name":"Winter","offerCode":"winter","offerMode":"PAY_UP_FRONT"}}}`)
	k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, `{"product_id":"`+offerTestProduct+`",
		"kind":"promotional","name":"Winter","identifier":"winter","mode":"PAY_UP_FRONT","duration":"THREE_MONTHS",
		"prices":[{"territory":"USA","price_point_id":"ppUSA"}],"confirm":true}`))
	body := bodyOf(t, k.ASC.Mutations()[0])
	inc, _ := body["included"].([]any)
	if len(inc) != 1 || reqRel(t, k.ASC.Mutations()[0], "subscription") != "subscriptions/s1" {
		t.Fatalf("body = %v", body)
	}
}

func TestOffersCreateWinBackEqualized(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/winBackOffers", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionPricePoints/{id}/equalizations", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionPricePoints","id":"ppGBR","attributes":{"customerPrice":"0.99"},"relationships":{"territory":{"data":{"type":"territories","id":"GBR"}}}},
		  {"type":"subscriptionPricePoints","id":"ppDEU","attributes":{"customerPrice":"1.09"},"relationships":{"territory":{"data":{"type":"territories","id":"DEU"}}}}]`,
		`{"type":"territories","id":"GBR","attributes":{"currency":"GBP"}}`, `{"type":"territories","id":"DEU","attributes":{"currency":"EUR"}}`))
	k.ASC.Handle(http.MethodPost, "/v1/winBackOffers", http.StatusCreated,
		`{"data":{"type":"winBackOffers","id":"wbN","attributes":{"referenceName":"Come back","offerId":"comeback","priority":"NORMAL"}}}`)

	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, `{"product_id":"`+offerTestProduct+`",
		"kind":"win_back","name":"Come back","identifier":"comeback","mode":"PAY_AS_YOU_GO","duration":"ONE_MONTH","periods":2,
		"start_date":"2026-11-01","paid_months":3,"lapsed_min_months":1,"lapsed_max_months":6,
		"prices":[{"territory":"USA","price_point_id":"ppUSA"}],"equalize":true,"confirm":true}`))
	tooltest.AssertGolden(t, "testdata/offers/winback_create_requests.json", offerMutationBodies(t, k))
	out := tooltest.Decode[offersCreateOut](t, res)
	if out.Created != 1 || out.Territories != 3 || out.Offer == nil || out.Offer.ID != "wbN" {
		t.Fatalf("out = %+v", out)
	}
}

func TestOffersCreateOfferCodeCampaignFreeTrial(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/offerCodes", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/subscriptionAvailability", http.StatusOK,
		`{"data":{"type":"subscriptionAvailabilities","id":"av1","attributes":{"availableInNewTerritories":true}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionAvailabilities/{id}/availableTerritories", http.StatusOK, jsonapiDoc(
		`[{"type":"territories","id":"USA"},{"type":"territories","id":"SWE"},{"type":"territories","id":"GBR"}]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionOfferCodes", http.StatusCreated,
		`{"data":{"type":"subscriptionOfferCodes","id":"ocN","attributes":{"name":"Spring","active":true}}}`)

	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, `{"product_id":"`+offerTestProduct+`",
		"kind":"offer_code","name":"Spring","mode":"FREE_TRIAL","duration":"ONE_MONTH",
		"customer_eligibilities":["NEW","EXPIRED"],"offer_eligibility":"STACK_WITH_INTRO_OFFERS",
		"prices":[{"territory":"USA"}],"equalize":true,"confirm":true}`))
	tooltest.AssertGolden(t, "testdata/offers/offer_code_create_requests.json", offerMutationBodies(t, k))
	if len(res.Next) != 1 || res.Next[0].Tool != "offer_codes_generate" || res.Next[0].Args["offer_code_id"] != "ocN" {
		t.Fatalf("next = %+v", res.Next)
	}
	if out := tooltest.Decode[offersCreateOut](t, res); out.Territories != 3 {
		t.Fatalf("out = %+v", out)
	}
}

func TestOffersCreateRejectsBeforeRequests(t *testing.T) {
	for name, args := range map[string]string{
		"win-back without eligibility": `{"product_id":"x","kind":"win_back","name":"a","identifier":"b","mode":"FREE_TRIAL","duration":"ONE_WEEK","start_date":"2026-11-01","prices":[{"territory":"USA"}]}`,
		"offer code without customers": `{"product_id":"x","kind":"offer_code","name":"a","offer_eligibility":"STACK_WITH_INTRO_OFFERS","mode":"FREE_TRIAL","duration":"ONE_WEEK","prices":[{"territory":"USA"}]}`,
		"promo with dates":             `{"product_id":"x","kind":"promotional","name":"a","identifier":"b","mode":"FREE_TRIAL","duration":"ONE_WEEK","start_date":"2026-11-01","prices":[{"territory":"USA"}]}`,
		"bad date":                     `{"product_id":"x","kind":"intro","mode":"FREE_TRIAL","duration":"ONE_WEEK","start_date":"11/01/2026","prices":[{"territory":"USA"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "offers_create"), k.Env, args), "invalid_arguments")
			if n := len(k.ASC.Requests()); n != 0 {
				t.Fatalf("%d requests before validation", n)
			}
		})
	}
}

func TestOfferCodesGenerateOneTimeWithValues(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionOfferCodeOneTimeUseCodes", http.StatusCreated,
		`{"data":{"type":"subscriptionOfferCodeOneTimeUseCodes","id":"otN","attributes":{"numberOfCodes":25,"active":true}}}`)
	csv := "Code\n"
	for i := range 25 {
		csv += "CODE" + string(rune('A'+i)) + "X9\n"
	}
	k.ASC.HandleFixture(http.MethodGet, "/v1/subscriptionOfferCodeOneTimeUseCodes/{id}/values", tooltest.Fixture{Status: http.StatusOK, Body: []byte(csv)})

	args := `{"offer_code_id":"oc1","type":"one_time","number_of_codes":25,"expiration_date":"2026-12-31"`
	prev := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env, args+`}`))
	if pv := tooltest.JSON(t, prev.Data); !strings.Contains(pv, `"needs_confirm"`) || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %s", pv)
	}
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env, args+`,"confirm":true}`))
	tooltest.AssertGolden(t, "testdata/offers/one_time_requests.json", offerMutationBodies(t, k))
	out := tooltest.Decode[offerCodesResultOut](t, res)
	if out.ID != "otN" || out.Count != 25 || out.Sensitive == "" || out.File != filepath.Join(k.Files.Root, "offer-codes", "otN.csv") {
		t.Fatalf("out = %+v", out)
	}
	// Code values go to the confined file, never to the agent.
	if js := tooltest.JSON(t, res.Data); strings.Contains(js, "CODEAX9") {
		t.Fatalf("result leaks code values: %s", js)
	}
	file, err := k.Files.ReadFile("offer-codes/otN.csv")
	if err != nil || !strings.HasPrefix(string(file), "code\nCODEAX9\n") || strings.Count(string(file), "\n") != 26 {
		t.Fatalf("codes file = %q, %v", file, err)
	}
	if got := k.ASC.RequestsTo(http.MethodGet, "/v1/subscriptionOfferCodeOneTimeUseCodes/{id}/values"); len(got) != 1 || got[0].Path != "/v1/subscriptionOfferCodeOneTimeUseCodes/otN/values" {
		t.Fatalf("values requests = %+v", got)
	}
}

func TestOfferCodesValuesPending(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionOfferCodeOneTimeUseCodes/{id}/values", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env, `{"batch_id":"otN"}`))
	out := tooltest.Decode[offerCodesResultOut](t, res)
	if !out.ValuesPending || len(res.Next) != 1 || res.Next[0].Args["batch_id"] != "otN" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("out = %+v next = %+v", out, res.Next)
	}
}

func TestOfferCodesGenerateCustomCode(t *testing.T) {
	k := offerKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/offerCodes", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionOfferCodes","id":"oc1","attributes":{"name":"Spring"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionOfferCodes/{id}/customCodes", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionOfferCodeCustomCodes", http.StatusCreated,
		`{"data":{"type":"subscriptionOfferCodeCustomCodes","id":"ccN","attributes":{"customCode":"HJULSPRING","numberOfCodes":500,"active":true}}}`)

	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env, `{"product_id":"`+offerTestProduct+`",
		"campaign":"Spring","type":"custom","custom_code":"HJULSPRING","number_of_codes":500,"expiration_date":"2027-03-31","confirm":true}`))
	tooltest.AssertGolden(t, "testdata/offers/custom_code_requests.json", offerMutationBodies(t, k))
	out := tooltest.Decode[offerCodesResultOut](t, res)
	if out.ID != "ccN" || out.OfferCodeID != "oc1" || out.CustomCode != "HJULSPRING" || out.File != "" {
		t.Fatalf("out = %+v", out)
	}
}

func TestOfferCodesCustomRepeatIsAlready(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionOfferCodes/{id}/customCodes", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionOfferCodeCustomCodes","id":"cc1","attributes":{"customCode":"HJULSPRING","numberOfCodes":500,"active":true}}]`))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env,
		`{"offer_code_id":"oc1","type":"custom","custom_code":"hjulspring","number_of_codes":500}`))
	if got := tooltest.Decode[map[string]any](t, res); got["already"] != true || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("result = %v", got)
	}
}

func TestOfferCodesGenerateRejectsBeforeRequests(t *testing.T) {
	k := newKit(t)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "offer_codes_generate"), k.Env,
		`{"offer_code_id":"oc1","type":"one_time","number_of_codes":5}`), "invalid_arguments")
	if n := len(k.ASC.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestOfferParseCodes(t *testing.T) {
	got, err := offerParseCodes([]byte("ABC123\n\nDEF456\n"))
	if err != nil || len(got) != 2 || got[1] != "DEF456" {
		t.Fatalf("got %v, %v", got, err)
	}
}
