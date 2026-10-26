package monetization

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const prcStateError = `{"errors":[{"status":"409","code":"STATE_ERROR","title":"wrong state","detail":"price change pending"}]}`

// prcSubPostsFailing answers subscription price POSTs with a 409 for the
// territories fail selects, 201 otherwise.
func prcSubPostsFailing(t *testing.T, k *tooltest.Kit, fail func(territory string) bool) {
	k.ASC.HandleFunc(http.MethodPost, "/v1/subscriptionPrices", func(r tool.Request) tooltest.Fixture {
		terr := reqRel(t, r, "territory")
		if fail(terr[strings.LastIndex(terr, "/")+1:]) {
			return tooltest.Fixture{Status: http.StatusConflict, Body: []byte(prcStateError)}
		}
		return tooltest.Fixture{Status: http.StatusCreated, Body: []byte(`{"data":{"type":"subscriptionPrices","id":"spN"}}`)}
	})
}

const prcSubArgs = `{"kind":"subscription","product_id":"se.jvcr.hjul.pro","price":"9.99"}`

func TestPricingApplyAllFailIsTypedError(t *testing.T) {
	k := newKit(t)
	prcSubCurrentFixture(k)
	prcSubPostsFailing(t, k, func(string) bool { return true })
	_, res := prcPlanThenApply(t, k, prcSubArgs)
	if !res.IsError {
		t.Fatalf("want error, got %s", tooltest.JSON(t, res.Data))
	}
	body := tooltest.Decode[struct {
		tool.ErrorBody
		Items pricingApplyOut `json:"items"`
	}](t, res)
	if body.Error != "api_error" || body.Code != "STATE_ERROR" || len(body.Items.Failed) != 3 {
		t.Errorf("body = %+v", body)
	}
	found := false
	for _, n := range res.Next {
		found = found || n.Tool == "pricing_plan"
	}
	if !found {
		t.Errorf("next = %+v, want pricing_plan", res.Next)
	}
	if n := len(k.ASC.Mutations()); n != 3 {
		t.Errorf("mutations = %d, want 3", n)
	}
}

func TestPricingApplyMixedIsPartialWithItemCode(t *testing.T) {
	k := newKit(t)
	prcSubCurrentFixture(k)
	prcSubPostsFailing(t, k, func(terr string) bool { return terr != "USA" })
	_, res := prcPlanThenApply(t, k, prcSubArgs)
	out := tooltest.Decode[pricingApplyOut](t, tooltest.MustOK(t, res))
	if out.Status != "partial" || len(out.Applied) != 1 || len(out.Failed) != 2 {
		t.Fatalf("out = %+v", out)
	}
	for _, f := range out.Failed {
		if f.Code == "" || f.Code == "error" {
			t.Errorf("failed %s code = %q", f.Territory, f.Code)
		}
	}
}
