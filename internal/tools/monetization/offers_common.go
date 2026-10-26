package monetization

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Offer kinds as reported by offers_list and accepted by offers_create.
const (
	offerKindIntro     = "intro"
	offerKindPromo     = "promotional"
	offerKindWinBack   = "win_back"
	offerKindCode      = "offer_code"
	offerKindIAPCode   = "iap_offer_code"
	offerModeFreeTrial = "FREE_TRIAL"
)

var (
	offerModes      = []any{"FREE_TRIAL", "PAY_AS_YOU_GO", "PAY_UP_FRONT"}
	offerDurations  = []any{"THREE_DAYS", "ONE_WEEK", "TWO_WEEKS", "ONE_MONTH", "TWO_MONTHS", "THREE_MONTHS", "SIX_MONTHS", "ONE_YEAR"}
	offerEligibles  = []any{"NEW", "EXISTING", "EXPIRED"}
	offerStacking   = []any{"STACK_WITH_INTRO_OFFERS", "REPLACE_INTRO_OFFERS"}
	offerPriorities = []any{"HIGH", "NORMAL"}
)

// offerSub is one subscription an offer belongs to.
type offerSub struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	Name      string `json:"name,omitempty"`
}

// offerPriceIn is one wanted territory price of an offer.
type offerPriceIn struct {
	Territory    string `json:"territory" jsonschema:"territory id, e.g. USA"`
	Price        string `json:"price,omitempty" jsonschema:"customer price in the territory currency, e.g. 0.99; resolved to Apple's price point"`
	PricePointID string `json:"price_point_id,omitempty" jsonschema:"subscriptionPricePoints id (instead of price)"`
}

// offerPrice is a resolved territory price. PricePoint is empty for free
// offers, which only name the territory.
type offerPrice struct {
	Territory     string `json:"territory"`
	PricePoint    string `json:"price_point,omitempty"`
	CustomerPrice string `json:"customer_price,omitempty"`
}

// offerSubs lists every subscription of the app across its groups.
func offerSubs(ctx context.Context, env *tool.Env, appID string) ([]offerSub, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/subscriptionGroups", tool.ListOptions{
		Query:   url.Values{"limit[subscriptions]": {"50"}},
		Include: []string{"subscriptions"},
		Fields: map[string][]string{
			"subscriptionGroups": {"referenceName", "subscriptions"},
			"subscriptions":      {"productId", "name"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	inc := indexIncluded(res.Included)
	var out []offerSub
	for _, g := range res.Data {
		for _, s := range related(g, "subscriptions", "subscriptions", inc) {
			out = append(out, offerSub{ID: s.ID, ProductID: s.Str("productId"), Name: s.Str("name")})
		}
	}
	return out, nil
}

// offerFindSub returns the app's subscription with productID; found is false
// when the app has no such subscription.
func offerFindSub(ctx context.Context, env *tool.Env, appID, productID string) (offerSub, bool, error) {
	subs, err := offerSubs(ctx, env, appID)
	if err != nil {
		return offerSub{}, false, err
	}
	for _, s := range subs {
		if s.ProductID == productID {
			return s, true, nil
		}
	}
	return offerSub{}, false, nil
}

// offerSubNotFound is the error for an unknown subscription product id.
func offerSubNotFound(productID string) error {
	return &tool.ErrNotFound{
		Kind: "subscription", Ref: productID,
		Hint: "offers exist on auto-renewable subscriptions; check the product id",
		Next: []tool.NextStep{{Tool: "subscriptions_list"}},
	}
}

// offerDate validates an optional YYYY-MM-DD argument.
func offerDate(arg, v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return tool.InvalidArgs("%s must be YYYY-MM-DD, got %q", arg, v)
	}
	return nil
}

// offerOne is a to-one relationship linkage.
func offerOne(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

// offerDoc builds a JSON:API create document with inline included resources.
func offerDoc(typ string, attrs, rels map[string]any, included []any) map[string]any {
	data := map[string]any{"type": typ, "attributes": attrs}
	if len(rels) > 0 {
		data["relationships"] = rels
	}
	doc := map[string]any{"data": data}
	if len(included) > 0 {
		doc["included"] = included
	}
	return doc
}

// offerInlinePrices renders prices as a to-many relationship of priceType
// plus the included inline-create resources (local ids "${price-N}").
func offerInlinePrices(priceType string, prices []offerPrice) (rel map[string]any, included []any) {
	links := make([]any, 0, len(prices))
	included = make([]any, 0, len(prices))
	for i, p := range prices {
		id := "${price-" + strconv.Itoa(i) + "}"
		links = append(links, map[string]any{"type": priceType, "id": id})
		rels := map[string]any{"territory": offerOne("territories", p.Territory)}
		if p.PricePoint != "" {
			rels["subscriptionPricePoint"] = offerOne("subscriptionPricePoints", p.PricePoint)
		}
		included = append(included, map[string]any{"type": priceType, "id": id, "relationships": rels})
	}
	return map[string]any{"data": links}, included
}

// offerPostDoc creates a resource from a full document and returns it.
func offerPostDoc(ctx context.Context, env *tool.Env, path, typ string, doc map[string]any) (tool.Resource, error) {
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: path, Write: true, Body: doc})
	if err != nil {
		return tool.Resource{}, err
	}
	r, ok := resp.First()
	if !ok || r.ID == "" {
		return tool.Resource{}, fmt.Errorf("create %s: response has no resource: %w", typ, tool.ErrOutcomeUnknown)
	}
	return r, nil
}

// offerResolvePrices turns wanted prices into territory price points. Paid
// modes need a price or price_point_id per territory; free trials may name
// territories only. equalize expands the single given price to every
// territory using Apple's equalized price points (or, for a free trial
// without a price, to every territory the subscription is available in).
func offerResolvePrices(ctx context.Context, env *tool.Env, subID, mode string, in []offerPriceIn, equalize bool) ([]offerPrice, error) {
	free := mode == offerModeFreeTrial
	if len(in) == 0 {
		return nil, tool.InvalidArgs("prices is required: [{territory, price}] (free trial: [{territory}]); equalize:true expands one entry to all territories")
	}
	if equalize && len(in) != 1 {
		return nil, tool.InvalidArgs("equalize needs exactly one prices entry (the base territory), got %d", len(in))
	}
	seen := map[string]bool{}
	out := make([]offerPrice, 0, len(in))
	for i, p := range in {
		terr := strings.ToUpper(strings.TrimSpace(p.Territory))
		if terr == "" {
			return nil, tool.InvalidArgs("prices[%d].territory is required", i)
		}
		if seen[terr] {
			return nil, tool.InvalidArgs("prices lists territory %s twice", terr)
		}
		seen[terr] = true
		r := offerPrice{Territory: terr, PricePoint: p.PricePointID}
		switch {
		case p.PricePointID != "":
		case p.Price != "":
			pp, err := offerFindPricePoint(ctx, env, subID, terr, p.Price)
			if err != nil {
				return nil, err
			}
			r = pp
		case !free:
			return nil, tool.InvalidArgs("prices[%d] (%s) needs price or price_point_id for a %s offer", i, terr, mode)
		}
		out = append(out, r)
	}
	if !equalize {
		return out, nil
	}
	base := out[0]
	var more []offerPrice
	var err error
	if base.PricePoint != "" {
		more, err = offerEqualizations(ctx, env, base.PricePoint)
	} else {
		more, err = offerAvailableTerritories(ctx, env, subID)
	}
	if err != nil {
		return nil, err
	}
	for _, m := range more {
		if !seen[m.Territory] {
			seen[m.Territory] = true
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out[1:], func(a, b offerPrice) int { return strings.Compare(a.Territory, b.Territory) })
	return out, nil
}

// offerFindPricePoint finds the subscription price point with the exact
// customer price in a territory.
func offerFindPricePoint(ctx context.Context, env *tool.Env, subID, territory, price string) (offerPrice, error) {
	want, ok := new(big.Rat).SetString(strings.TrimSpace(price))
	if !ok {
		return offerPrice{}, tool.InvalidArgs("price %q for %s is not a decimal number", price, territory)
	}
	res, err := env.ASC.List(ctx, "/v1/subscriptions/"+url.PathEscape(subID)+"/pricePoints", tool.ListOptions{
		Query:    url.Values{"filter[territory]": {territory}},
		Fields:   map[string][]string{"subscriptionPricePoints": {"customerPrice"}},
		MaxItems: 2000,
	})
	if err != nil {
		return offerPrice{}, fmt.Errorf("list price points for %s: %w", territory, err)
	}
	var below, above string
	var lo, hi *big.Rat
	for _, pp := range res.Data {
		got, ok := new(big.Rat).SetString(pp.Str("customerPrice"))
		if !ok {
			continue
		}
		switch got.Cmp(want) {
		case 0:
			return offerPrice{Territory: territory, PricePoint: pp.ID, CustomerPrice: pp.Str("customerPrice")}, nil
		case -1:
			if lo == nil || got.Cmp(lo) > 0 {
				lo, below = got, pp.Str("customerPrice")
			}
		default:
			if hi == nil || got.Cmp(hi) < 0 {
				hi, above = got, pp.Str("customerPrice")
			}
		}
	}
	return offerPrice{}, tool.InvalidArgs("no %s price point equals %s (nearest: %q below, %q above)", territory, price, below, above)
}

// offerEqualizations returns Apple's equalized price points of base in every
// other territory.
func offerEqualizations(ctx context.Context, env *tool.Env, base string) ([]offerPrice, error) {
	res, err := env.ASC.List(ctx, "/v1/subscriptionPricePoints/"+url.PathEscape(base)+"/equalizations", tool.ListOptions{
		Include:  []string{"territory"},
		Fields:   map[string][]string{"subscriptionPricePoints": {"customerPrice", "territory"}, "territories": {"currency"}},
		MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list equalized price points: %w", err)
	}
	out := make([]offerPrice, 0, len(res.Data))
	for _, pp := range res.Data {
		if t := pp.RelID("territory"); t != "" {
			out = append(out, offerPrice{Territory: t, PricePoint: pp.ID, CustomerPrice: pp.Str("customerPrice")})
		}
	}
	return out, nil
}

// offerAvailableTerritories returns the territories the subscription is sold
// in (for free offers, which carry no price point).
func offerAvailableTerritories(ctx context.Context, env *tool.Env, subID string) ([]offerPrice, error) {
	resp, err := env.ASC.Get(ctx, "/v1/subscriptions/"+url.PathEscape(subID)+"/subscriptionAvailability", nil)
	if err != nil {
		return nil, fmt.Errorf("read subscription availability: %w", err)
	}
	av, ok := resp.First()
	if !ok {
		return nil, tool.InvalidArgs("subscription %s has no availability; set it with pricing_apply first", subID)
	}
	res, err := env.ASC.List(ctx, "/v1/subscriptionAvailabilities/"+url.PathEscape(av.ID)+"/availableTerritories", tool.ListOptions{
		Fields: map[string][]string{"territories": {"currency"}}, MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list available territories: %w", err)
	}
	out := make([]offerPrice, 0, len(res.Data))
	for _, t := range res.Data {
		out = append(out, offerPrice{Territory: t.ID})
	}
	return out, nil
}
