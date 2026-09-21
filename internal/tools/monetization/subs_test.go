package monetization

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool/tooltest"
)

const subsTestProduct = "se.jvcr.hjul.plus.year"

func TestSubscriptionsListCounts(t *testing.T) {
	k := newKit(t)
	k.ASC.HandleFile(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", "testdata/catalog/subscription_groups.json")
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/g2/subscriptions", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.HandleFile(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", "testdata/catalog/group_subscriptions.json")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_list"), k.Env, `{}`))
	equalLines(t, requestLog(k.ASC.Requests()), []string{
		"GET /v1/apps/app1/subscriptionGroups",
		"GET /v1/subscriptionGroups/g1/subscriptions",
		"GET /v1/subscriptionGroups/g2/subscriptions",
	})
	out := tooltest.Decode[subsListOut](t, res)
	if len(out.Groups) != 2 || out.Subscriptions != 2 || out.ByState["APPROVED"] != 1 || out.ByState["MISSING_METADATA"] != 1 {
		t.Fatalf("counts = %+v", out)
	}
	g := out.Groups[0]
	if len(g.Localizations) != 1 || g.Localizations[0].CustomAppName != "Hjul" {
		t.Fatalf("group localizations = %+v", g.Localizations)
	}
	y := g.Subscriptions[0]
	if y.ProductID != subsTestProduct || y.Period != "ONE_YEAR" || y.GroupLevel != 1 || y.Version == nil || y.Version.State != "APPROVED" ||
		y.Offers.Introductory != 2 || y.Offers.OfferCodes != 1 {
		t.Fatalf("yearly = %+v", y)
	}
	if len(out.Groups[1].Subscriptions) != 0 {
		t.Fatalf("legacy group = %+v", out.Groups[1])
	}
}

func TestSubscriptionsUpsertCreatesGroupAndSubscription(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionGroups", http.StatusCreated,
		`{"data":{"type":"subscriptionGroups","id":"gN","attributes":{"referenceName":"Hjul Plus"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/versions", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptionGroupVersions", http.StatusCreated,
		`{"data":{"type":"subscriptionGroupVersions","id":"gvN","attributes":{"version":1,"state":"PREPARE_FOR_SUBMISSION"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroupVersions/{id}/localizations", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v2/subscriptionGroupLocalizations", http.StatusCreated,
		`{"data":{"type":"subscriptionGroupLocalizations","id":"glN"}}`)
	k.ASC.Handle(http.MethodPost, "/v1/subscriptions", http.StatusCreated,
		`{"data":{"type":"subscriptions","id":"sN","attributes":{"productId":"`+subsTestProduct+`","state":"MISSING_METADATA"}}}`)
	// Apple created the first subscription version along with the product.
	k.ASC.Handle(http.MethodGet, "/v1/subscriptions/{id}/versions", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionVersions","id":"svN","attributes":{"version":1,"state":"PREPARE_FOR_SUBMISSION"},
		 "relationships":{"localizations":{"data":[]}}}]`))
	k.ASC.Handle(http.MethodPost, "/v2/subscriptionLocalizations", http.StatusCreated,
		`{"data":{"type":"subscriptionLocalizations","id":"slN"}}`)

	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, `{
		"group":"Hjul Plus","group_localizations":{"en-US":{"name":"Hjul Plus","custom_app_name":"Hjul"}},
		"product_id":"`+subsTestProduct+`","name":"Yearly","period":"ONE_YEAR","group_level":1,
		"localizations":{"en-US":{"name":"Plus yearly","description":"All features, billed yearly"}},"confirm":true}`))

	equalLines(t, requestLog(k.ASC.Requests()), []string{
		"GET /v1/apps/app1/subscriptionGroups",
		"POST /v1/subscriptionGroups",
		"GET /v1/subscriptionGroups/gN/versions",
		"POST /v1/subscriptionGroupVersions",
		"GET /v1/subscriptionGroupVersions/gvN/localizations",
		"POST /v2/subscriptionGroupLocalizations",
		"POST /v1/subscriptions",
		"GET /v1/subscriptions/sN/versions",
		"POST /v2/subscriptionLocalizations",
	})
	reqs := k.ASC.Requests()
	if got := reqRel(t, reqs[1], "app"); got != "apps/app1" {
		t.Fatalf("group app rel = %s", got)
	}
	if got := reqRel(t, reqs[5], "version"); got != "subscriptionGroupVersions/gvN" {
		t.Fatalf("group localization must attach to the group version, got %s", got)
	}
	if a := reqAttrs(t, reqs[5]); a["customAppName"] != "Hjul" || a["locale"] != "en-US" {
		t.Fatalf("group localization attrs = %v", a)
	}
	if got := reqRel(t, reqs[6], "group"); got != "subscriptionGroups/gN" {
		t.Fatalf("subscription group rel = %s", got)
	}
	if a := reqAttrs(t, reqs[6]); a["subscriptionPeriod"] != "ONE_YEAR" || a["groupLevel"] != float64(1) || a["name"] != "Yearly" {
		t.Fatalf("subscription attrs = %v", a)
	}
	if got := reqRel(t, reqs[8], "version"); got != "subscriptionVersions/svN" {
		t.Fatalf("subscription localization must attach to the version, got %s", got)
	}
	out := tooltest.Decode[subsUpsertOut](t, res)
	if out.Group.Action != actionCreated || out.Subscription.Action != actionCreated || out.Version.Created || !out.GroupVersion.Created {
		t.Fatalf("out = %+v", out)
	}
}

func TestSubscriptionsUpsertExistingPatchesDiff(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptions","id":"s1","attributes":{"name":"Yearly","productId":"`+subsTestProduct+`","subscriptionPeriod":"ONE_YEAR","groupLevel":2}}]`))
	k.ASC.Handle(http.MethodPatch, "/v1/subscriptions/{id}", http.StatusOK, `{"data":{"type":"subscriptions","id":"s1"}}`)
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","name":"Yearly","period":"ONE_YEAR","group_level":1,"confirm":true}`))
	equalLines(t, requestLog(k.ASC.Mutations()), []string{"PATCH /v1/subscriptions/s1"})
	if a := reqAttrs(t, k.ASC.Mutations()[0]); len(a) != 1 || a["groupLevel"] != float64(1) {
		t.Fatalf("PATCH attrs = %v", a)
	}
	out := tooltest.Decode[subsUpsertOut](t, res)
	if out.Group.Action != actionUnchanged || out.Subscription.Action != actionUpdated {
		t.Fatalf("out = %+v", out)
	}

	// The same call again changes nothing.
	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k2.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptions","id":"s1","attributes":{"name":"Yearly","productId":"`+subsTestProduct+`","subscriptionPeriod":"ONE_YEAR","groupLevel":1}}]`))
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k2.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","name":"Yearly","period":"ONE_YEAR","group_level":1}`))
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true || len(k2.ASC.Mutations()) != 0 {
		t.Fatalf("want already without mutations, got %v", d)
	}
}

func TestSubscriptionsUpsertConflictInOtherGroup(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups?filter[referenceName]=Hjul Plus", http.StatusOK,
		jsonapiDoc(`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"}}]`))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/subscriptionGroups", http.StatusOK, jsonapiDoc(
		`[{"type":"subscriptionGroups","id":"g1","attributes":{"referenceName":"Hjul Plus"},"relationships":{"subscriptions":{"data":[]}}},
		  {"type":"subscriptionGroups","id":"g2","attributes":{"referenceName":"Legacy"},"relationships":{"subscriptions":{"data":[{"type":"subscriptions","id":"sx"}]}}}]`,
		`{"type":"subscriptions","id":"sx","attributes":{"productId":"`+subsTestProduct+`"}}`))
	k.ASC.Handle(http.MethodGet, "/v1/subscriptionGroups/{id}/subscriptions", http.StatusOK, jsonapiDoc(`[]`))
	k.ASC.Handle(http.MethodPost, "/v1/subscriptions", http.StatusConflict, conflictBody)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env,
		`{"group":"Hjul Plus","product_id":"`+subsTestProduct+`","name":"Yearly","confirm":true}`)
	if !res.IsError || !strings.Contains(tooltest.JSON(t, res.Data), "another group (g2)") {
		t.Fatalf("want another-group error, got %s", tooltest.JSON(t, res.Data))
	}
	if n := len(k.ASC.RequestsTo(http.MethodPost, "/v1/subscriptions")); n != 1 {
		t.Fatalf("POSTs = %d, want exactly 1 (never replayed)", n)
	}
}

func TestSubscriptionsUpsertRejectsBeforeRequests(t *testing.T) {
	cases := map[string]string{
		"display name":  `{"group":"G","product_id":"p.x","localizations":{"en-US":{"name":"` + strings.Repeat("n", 31) + `"}}}`,
		"group name":    `{"group":"` + strings.Repeat("g", 65) + `"}`,
		"needs product": `{"group":"G","name":"Yearly"}`,
		"group level":   `{"group":"G","product_id":"p.x","group_level":0}`,
		"group locale":  `{"group":"G","group_localizations":{"xx-nope":{"name":"G"}}}`,
		"bad period":    `{"group":"G","product_id":"p.x","period":"TEN_YEARS"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			res := tooltest.Call(t, mustTool(t, "subscriptions_upsert"), k.Env, args)
			if !res.IsError {
				t.Fatalf("want error, got %s", tooltest.JSON(t, res.Data))
			}
			if n := len(k.ASC.Requests()); n != 0 {
				t.Fatalf("%d requests sent before validation", n)
			}
		})
	}
}
