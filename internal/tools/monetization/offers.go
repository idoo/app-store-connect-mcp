package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

const (
	// offerMaxSubs bounds the subscriptions offers_list expands app-wide
	// (four requests each).
	offerMaxSubs = 10
	// offerRelCap is Apple's limit[prices] maximum; a count at the cap means
	// "at least".
	offerRelCap = 50
	// offerShowIDs is the most territory or offer ids echoed per row.
	offerShowIDs = 10
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "offers_list", Toolset: tool.Monetization, Class: tool.Read, Idempotent: true,
		Title: "List subscription offers",
		Description: "Introductory (grouped by terms), promotional, win-back and offer-code offers of one subscription " +
			"(product_id) or every subscription of the app: mode, duration, periods, eligibility, territory counts, " +
			"code counts. An IAP product_id lists its offer codes.",
	}, offersList))
	tool.Register(tool.New(tool.Meta{
		Name: "offers_create", Toolset: tool.Monetization, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Create a subscription offer",
		Description: "Create an intro (per territory), promotional, win-back or offer-code offer on a subscription. " +
			"Prices by customer price or price_point_id; equalize:true expands one price to all territories. Needs " +
			"confirm:true and human approval; a same-name offer is already.",
		Enums: map[string][]any{
			"kind": {offerKindIntro, offerKindPromo, offerKindWinBack, offerKindCode}, "mode": offerModes,
			"duration": offerDurations, "customer_eligibilities": offerEligibles, "offer_eligibility": offerStacking,
			"priority": offerPriorities,
		},
	}, offersCreate))
}

type offersListIn struct {
	App       tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	ProductID string      `json:"product_id,omitempty" jsonschema:"subscription (or IAP) product id; default: every subscription of the app"`
}

type offerCodesOut struct {
	Total          int      `json:"total,omitempty"`
	Production     int      `json:"production,omitempty"`
	Sandbox        int      `json:"sandbox,omitempty"`
	OneTimeBatches int      `json:"one_time_batches"`
	CustomCodes    []string `json:"custom_codes,omitempty"`
}

type offerOut struct {
	Kind         string         `json:"kind"`
	ID           string         `json:"id,omitempty"`
	IDs          []string       `json:"ids,omitempty"`
	Name         string         `json:"name,omitempty"`
	Identifier   string         `json:"identifier,omitempty"`
	Mode         string         `json:"mode,omitempty"`
	Duration     string         `json:"duration,omitempty"`
	Periods      int            `json:"periods,omitempty"`
	StartDate    string         `json:"start_date,omitempty"`
	EndDate      string         `json:"end_date,omitempty"`
	Territories  int            `json:"territories"`
	AtLeast      bool           `json:"territories_at_least,omitempty"`
	TerritoryIDs []string       `json:"territory_ids,omitempty"`
	Prices       []string       `json:"prices,omitempty"`
	Eligibility  map[string]any `json:"eligibility,omitempty"`
	Priority     string         `json:"priority,omitempty"`
	Active       *bool          `json:"active,omitempty"`
	Codes        *offerCodesOut `json:"codes,omitempty"`
}

type offerSubOut struct {
	offerSub
	Counts map[string]int `json:"counts"`
	Offers []offerOut     `json:"offers"`
}

type offerIAPOut struct {
	ID        string     `json:"id"`
	ProductID string     `json:"product_id"`
	Offers    []offerOut `json:"offers"`
}

type offersListOut struct {
	App           tool.App      `json:"app"`
	Subscriptions []offerSubOut `json:"subscriptions,omitempty"`
	IAP           *offerIAPOut  `json:"iap,omitempty"`
}

func offersList(ctx context.Context, env *tool.Env, in offersListIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	subs, err := offerSubs(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	out := offersListOut{App: app, Subscriptions: []offerSubOut{}}
	res := tool.OK(&out)
	if in.ProductID != "" {
		i := slices.IndexFunc(subs, func(s offerSub) bool { return s.ProductID == in.ProductID })
		if i < 0 {
			return offerIAPCodes(ctx, env, app, in.ProductID)
		}
		subs = subs[i : i+1]
	}
	if len(subs) > offerMaxSubs {
		res.Warn(strconv.Itoa(len(subs)-offerMaxSubs) + " subscription(s) not listed; pass product_id")
		subs = subs[:offerMaxSubs]
	}
	for _, s := range subs {
		so := offerSubOut{offerSub: s, Counts: map[string]int{}, Offers: []offerOut{}}
		for _, kind := range []string{offerKindIntro, offerKindPromo, offerKindWinBack, offerKindCode} {
			rows, err := offerListKind(ctx, env, s.ID, kind)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				so.Counts[kind] += max(1, len(r.IDs))
			}
			so.Offers = append(so.Offers, rows...)
		}
		out.Subscriptions = append(out.Subscriptions, so)
	}
	return res, nil
}

// offerIAPCodes lists the offer codes of a (non-subscription) in-app purchase.
func offerIAPCodes(ctx context.Context, env *tool.Env, app tool.App, productID string) (*tool.Result, error) {
	iap, _, found, err := iapFind(ctx, env, app.ID, productID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &tool.ErrNotFound{
			Kind: "product", Ref: productID, Hint: "no subscription or in-app purchase has this product id",
			Next: []tool.NextStep{{Tool: "subscriptions_list"}, {Tool: "iap_list"}},
		}
	}
	res, err := env.ASC.List(ctx, "/v2/inAppPurchases/"+url.PathEscape(iap.ID)+"/offerCodes", tool.ListOptions{
		Query:   url.Values{"limit[oneTimeUseCodes]": {"50"}, "limit[customCodes]": {"50"}, "limit[prices]": {"50"}},
		Include: []string{"oneTimeUseCodes", "customCodes", "prices"},
		Fields: map[string][]string{
			"inAppPurchaseOfferCodes": {
				"name", "customerEligibilities", "productionCodeCount", "sandboxCodeCount", "active",
				"oneTimeUseCodes", "customCodes", "prices",
			},
			"inAppPurchaseOfferCodeCustomCodes": {"customCode", "numberOfCodes", "expirationDate", "active"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list IAP offer codes: %w", err)
	}
	inc := s12aIncluded(res.Included)
	out := offerIAPOut{ID: iap.ID, ProductID: productID, Offers: []offerOut{}}
	for _, r := range res.Data {
		o := offerRow(offerKindIAPCode, r, inc)
		out.Offers = append(out.Offers, o)
	}
	return tool.OK(offersListOut{App: app, IAP: &out}), nil
}

// offerListKind lists one kind of offer on a subscription as output rows
// (intro offers grouped by identical terms).
func offerListKind(ctx context.Context, env *tool.Env, subID, kind string) ([]offerOut, error) {
	rs, inc, err := offerFetch(ctx, env, subID, kind)
	if err != nil {
		return nil, err
	}
	rows := make([]offerOut, 0, len(rs))
	for _, r := range rs {
		rows = append(rows, offerRow(kind, r, inc))
	}
	if kind == offerKindIntro {
		rows = offerGroupIntros(rows)
	}
	return rows, nil
}

// offerFetch reads the raw offers of one kind with what offerRow needs.
func offerFetch(ctx context.Context, env *tool.Env, subID, kind string) ([]tool.Resource, map[string]tool.Resource, error) {
	base := "/v1/subscriptions/" + url.PathEscape(subID)
	var path string
	var opts tool.ListOptions
	switch kind {
	case offerKindIntro:
		path = base + "/introductoryOffers"
		opts = tool.ListOptions{
			Include: []string{"territory", "subscriptionPricePoint"},
			Fields: map[string][]string{
				"subscriptionIntroductoryOffers": {"startDate", "endDate", "duration", "offerMode", "numberOfPeriods", "territory", "subscriptionPricePoint"},
				"subscriptionPricePoints":        {"customerPrice"},
				"territories":                    {"currency"},
			},
			MaxItems: 1000,
		}
	case offerKindPromo:
		path = base + "/promotionalOffers"
		opts = tool.ListOptions{
			Query:   url.Values{"limit[prices]": {strconv.Itoa(offerRelCap)}},
			Include: []string{"prices"},
			Fields: map[string][]string{
				"subscriptionPromotionalOffers":      {"duration", "name", "numberOfPeriods", "offerCode", "offerMode", "prices"},
				"subscriptionPromotionalOfferPrices": {"territory"},
			},
		}
	case offerKindWinBack:
		path = base + "/winBackOffers"
		opts = tool.ListOptions{
			Query:   url.Values{"limit[prices]": {strconv.Itoa(offerRelCap)}},
			Include: []string{"prices"},
			Fields: map[string][]string{
				"winBackOffers": {
					"referenceName", "offerId", "duration", "offerMode", "periodCount",
					"customerEligibilityPaidSubscriptionDurationInMonths", "customerEligibilityTimeSinceLastSubscribedInMonths",
					"customerEligibilityWaitBetweenOffersInMonths", "startDate", "endDate", "priority", "prices",
				},
				"winBackOfferPrices": {"territory"},
			},
		}
	default:
		path = base + "/offerCodes"
		opts = tool.ListOptions{
			Query: url.Values{
				"limit[prices]": {strconv.Itoa(offerRelCap)}, "limit[oneTimeUseCodes]": {"50"}, "limit[customCodes]": {"50"},
			},
			Include: []string{"oneTimeUseCodes", "customCodes", "prices"},
			Fields: map[string][]string{
				"subscriptionOfferCodes": {
					"name", "customerEligibilities", "offerEligibility", "duration", "offerMode", "numberOfPeriods",
					"totalNumberOfCodes", "productionCodeCount", "sandboxCodeCount", "active", "oneTimeUseCodes", "customCodes", "prices",
				},
				"subscriptionOfferCodeCustomCodes": {"customCode", "numberOfCodes", "expirationDate", "active"},
			},
		}
	}
	res, err := env.ASC.List(ctx, path, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("list %s offers: %w", kind, err)
	}
	return res.Data, s12aIncluded(res.Included), nil
}

// offerRow summarizes one offer resource.
func offerRow(kind string, r tool.Resource, inc map[string]tool.Resource) offerOut {
	o := offerOut{
		Kind: kind, ID: r.ID, Mode: r.Str("offerMode"), Duration: r.Str("duration"),
		StartDate: r.Str("startDate"), EndDate: r.Str("endDate"),
	}
	o.Periods, _ = r.Int("numberOfPeriods")
	if prices := r.RelIDs("prices"); len(prices) > 0 {
		o.Territories = len(prices)
		o.AtLeast = len(prices) >= offerRelCap
	}
	switch kind {
	case offerKindIntro:
		if t := r.RelID("territory"); t != "" {
			o.Territories, o.TerritoryIDs = 1, []string{t}
			if pp, ok := inc["subscriptionPricePoints/"+r.RelID("subscriptionPricePoint")]; ok && pp.Str("customerPrice") != "" {
				o.Prices = []string{t + " " + pp.Str("customerPrice")}
			}
		}
	case offerKindPromo:
		o.Name, o.Identifier = r.Str("name"), r.Str("offerCode")
	case offerKindWinBack:
		o.Name, o.Identifier, o.Priority = r.Str("referenceName"), r.Str("offerId"), r.Str("priority")
		o.Periods, _ = r.Int("periodCount")
		o.Eligibility = map[string]any{}
		for k, name := range map[string]string{
			"customerEligibilityPaidSubscriptionDurationInMonths": "paid_months",
			"customerEligibilityTimeSinceLastSubscribedInMonths":  "lapsed_months",
			"customerEligibilityWaitBetweenOffersInMonths":        "wait_months",
		} {
			if v, ok := r.Attributes[k]; ok && v != nil {
				o.Eligibility[name] = v
			}
		}
	case offerKindCode, offerKindIAPCode:
		o.Name = r.Str("name")
		o.Eligibility = map[string]any{}
		if v, ok := r.Attributes["customerEligibilities"]; ok && v != nil {
			o.Eligibility["customers"] = v
		}
		if v := r.Str("offerEligibility"); v != "" {
			o.Eligibility["offer"] = v
		}
		if a, ok := r.Bool("active"); ok {
			o.Active = &a
		}
		c := &offerCodesOut{OneTimeBatches: len(r.RelIDs("oneTimeUseCodes"))}
		c.Total, _ = r.Int("totalNumberOfCodes")
		c.Production, _ = r.Int("productionCodeCount")
		c.Sandbox, _ = r.Int("sandboxCodeCount")
		customType := "subscriptionOfferCodeCustomCodes"
		if kind == offerKindIAPCode {
			customType = "inAppPurchaseOfferCodeCustomCodes"
		}
		for _, cc := range s12aRelated(r, "customCodes", customType, inc) {
			c.CustomCodes = append(c.CustomCodes, cc.Str("customCode"))
		}
		o.Codes = c
	}
	if len(o.Eligibility) == 0 {
		o.Eligibility = nil
	}
	return o
}

// offerGroupIntros merges per-territory intro offers with identical terms.
func offerGroupIntros(rows []offerOut) []offerOut {
	var out []offerOut
	idx := map[string]int{}
	for _, r := range rows {
		key := strings.Join([]string{r.Mode, r.Duration, strconv.Itoa(r.Periods), r.StartDate, r.EndDate}, "|")
		i, ok := idx[key]
		if !ok {
			idx[key] = len(out)
			g := r
			g.ID, g.IDs = "", []string{r.ID}
			out = append(out, g)
			continue
		}
		g := &out[i]
		g.IDs = append(g.IDs, r.ID)
		g.Territories += r.Territories
		g.TerritoryIDs = append(g.TerritoryIDs, r.TerritoryIDs...)
		g.Prices = append(g.Prices, r.Prices...)
	}
	for i := range out {
		g := &out[i]
		slices.Sort(g.TerritoryIDs)
		slices.Sort(g.Prices)
		if len(g.IDs) == 1 {
			g.ID, g.IDs = g.IDs[0], nil
		}
		if len(g.IDs) > offerShowIDs {
			g.IDs = nil
		}
		if len(g.TerritoryIDs) > offerShowIDs {
			g.TerritoryIDs = g.TerritoryIDs[:offerShowIDs]
		}
		if len(g.Prices) > offerShowIDs {
			g.Prices = g.Prices[:offerShowIDs]
		}
	}
	return out
}

type offersCreateIn struct {
	App       tool.AppRef    `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	ProductID string         `json:"product_id" jsonschema:"subscription product id"`
	Kind      string         `json:"kind"`
	Mode      string         `json:"mode"`
	Duration  string         `json:"duration" jsonschema:"length of one offer period"`
	Periods   int            `json:"periods,omitempty" jsonschema:"number of periods; default 1"`
	Prices    []offerPriceIn `json:"prices" jsonschema:"territory prices; free trial: territories only"`
	Equalize  bool           `json:"equalize,omitempty" jsonschema:"expand the single prices entry to all territories (Apple equalization)"`
	Name      string         `json:"name,omitempty" jsonschema:"promotional name, win-back reference name or offer-code campaign name"`
	ID        string         `json:"identifier,omitempty" jsonschema:"promotional offer code / win-back offer id used in StoreKit"`
	StartDate string         `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD (intro, win-back: required)"`
	EndDate   string         `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD"`
	// Offer codes.
	Customers        []string `json:"customer_eligibilities,omitempty" jsonschema:"offer code: who may redeem"`
	OfferEligibility string   `json:"offer_eligibility,omitempty" jsonschema:"offer code: stack with or replace intro offers"`
	AutoRenew        *bool    `json:"auto_renew,omitempty" jsonschema:"offer code: renew after the offer"`
	// Win-back.
	PaidMonths      int    `json:"paid_months,omitempty" jsonschema:"win-back: minimum months previously paid"`
	LapsedMinMonths int    `json:"lapsed_min_months,omitempty" jsonschema:"win-back: minimum months since the subscription lapsed"`
	LapsedMaxMonths int    `json:"lapsed_max_months,omitempty" jsonschema:"win-back: maximum months since the subscription lapsed"`
	WaitMonths      *int   `json:"wait_months,omitempty" jsonschema:"win-back: months between two win-back offers"`
	Priority        string `json:"priority,omitempty" jsonschema:"win-back: default NORMAL"`
	tool.ConfirmFlag
}

type offersCreateOut struct {
	App          tool.App     `json:"app"`
	Subscription offerSub     `json:"subscription"`
	Kind         string       `json:"kind"`
	Offer        *offerOut    `json:"offer,omitempty"`
	Created      int          `json:"created"`
	IDs          []string     `json:"ids,omitempty"`
	Territories  int          `json:"territories"`
	Skipped      []string     `json:"skipped_territories,omitempty"`
	Prices       []offerPrice `json:"prices,omitempty"`
}

// offerSpec is the per-kind create shape.
type offerSpec struct {
	path, typ, priceType string
	match                func(tool.Resource) bool
}

func offersCreate(ctx context.Context, env *tool.Env, in offersCreateIn) (*tool.Result, error) {
	attrs, sp, err := offerValidate(in)
	if err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	sub, found, err := offerFindSub(ctx, env, app.ID, in.ProductID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, offerSubNotFound(in.ProductID)
	}
	out := offersCreateOut{App: app, Subscription: sub, Kind: in.Kind}
	if in.Kind == offerKindIntro {
		return offerCreateIntros(ctx, env, in, attrs, out)
	}
	if r, ok, err := offerExisting(ctx, env, sub.ID, in.Kind, sp.match); err != nil || ok {
		if err != nil {
			return nil, err
		}
		out.Offer = &r
		out.Territories = r.Territories
		return tool.Already(out), nil
	}
	prices, err := offerResolvePrices(ctx, env, sub.ID, in.Mode, in.Prices, in.Equalize)
	if err != nil {
		return nil, err
	}
	wctx, gres := offerConfirm(ctx, env, in, out.Subscription, attrs, prices)
	if gres != nil {
		return gres, nil
	}
	rels, included := offerInlinePrices(sp.priceType, prices)
	doc := offerDoc(sp.typ, attrs, map[string]any{
		"subscription": offerOne("subscriptions", sub.ID), "prices": rels,
	}, included)
	created, err := offerPostDoc(wctx, env, sp.path, sp.typ, doc)
	if err != nil {
		if s12aConflict(err) {
			if r, ok, rerr := offerExisting(ctx, env, sub.ID, in.Kind, sp.match); rerr == nil && ok {
				out.Offer = &r
				out.Territories = r.Territories
				return tool.Already(out), nil
			}
		}
		return nil, tool.WithNext(fmt.Errorf("create %s offer: %w", in.Kind, err),
			tool.NextStep{Tool: "offers_list", Args: map[string]any{"product_id": in.ProductID}})
	}
	row := offerRow(in.Kind, created, nil)
	row.Territories, row.AtLeast = len(prices), false
	out.Offer, out.Created, out.Territories = &row, 1, len(prices)
	out.IDs = []string{created.ID}
	if len(prices) <= offerShowIDs {
		out.Prices = prices
	}
	res := tool.OK(out)
	if in.Kind == offerKindCode {
		res.Then(tool.NextStep{Tool: "offer_codes_generate", Args: map[string]any{"offer_code_id": created.ID}})
	}
	return res, nil
}

// offerConfirm is the human gate of offers_create: offers change what
// customers pay. The preview lists the offer terms and every territory price.
func offerConfirm(ctx context.Context, env *tool.Env, in offersCreateIn, sub offerSub, attrs map[string]any, prices []offerPrice) (context.Context, *tool.Result) {
	terr := make(map[string]string, len(prices))
	for _, p := range prices {
		v := p.CustomerPrice
		switch {
		case v == "" && p.PricePoint != "":
			v = "price point " + p.PricePoint
		case v == "":
			v = "free"
		}
		terr[p.Territory] = v
	}
	return tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary:  fmt.Sprintf("create a %s offer on subscription %s in %d territories", in.Kind, in.ProductID, len(prices)),
		Affected: []string{"subscription " + in.ProductID + " (" + sub.ID + ")"},
		Intended: map[string]any{"terms": attrs, "prices": terr},
	})
}

// offerExisting finds an existing offer of kind matching an identity.
func offerExisting(ctx context.Context, env *tool.Env, subID, kind string, match func(tool.Resource) bool) (offerOut, bool, error) {
	rs, inc, err := offerFetch(ctx, env, subID, kind)
	if err != nil {
		return offerOut{}, false, err
	}
	for _, r := range rs {
		if match(r) {
			return offerRow(kind, r, inc), true, nil
		}
	}
	return offerOut{}, false, nil
}

// offerValidate checks the arguments for the kind and returns the create
// attributes and per-kind shape. Nothing is requested before it passes.
func offerValidate(in offersCreateIn) (map[string]any, offerSpec, error) {
	periods := in.Periods
	if periods == 0 {
		periods = 1
	}
	if periods < 1 {
		return nil, offerSpec{}, s12aInvalid("periods must be ≥ 1")
	}
	if strings.TrimSpace(in.ProductID) == "" {
		return nil, offerSpec{}, s12aInvalid("product_id is required")
	}
	for arg, v := range map[string]string{"start_date": in.StartDate, "end_date": in.EndDate} {
		if err := offerDate(arg, v); err != nil {
			return nil, offerSpec{}, err
		}
	}
	attrs := map[string]any{"offerMode": in.Mode, "duration": in.Duration}
	if in.StartDate != "" {
		attrs["startDate"] = in.StartDate
	}
	if in.EndDate != "" {
		attrs["endDate"] = in.EndDate
	}
	need := func(arg, v string) error {
		if strings.TrimSpace(v) == "" {
			return s12aInvalid("%s is required for a %s offer", arg, in.Kind)
		}
		return nil
	}
	var sp offerSpec
	switch in.Kind {
	case offerKindIntro:
		attrs["numberOfPeriods"] = periods
	case offerKindPromo:
		if err := offerFirstErr(need("name", in.Name), need("identifier", in.ID)); err != nil {
			return nil, sp, err
		}
		delete(attrs, "startDate")
		delete(attrs, "endDate")
		if in.StartDate != "" || in.EndDate != "" {
			return nil, sp, s12aInvalid("promotional offers have no start/end date")
		}
		attrs["name"], attrs["offerCode"], attrs["numberOfPeriods"] = in.Name, in.ID, periods
		sp = offerSpec{
			path: "/v1/subscriptionPromotionalOffers", typ: "subscriptionPromotionalOffers", priceType: "subscriptionPromotionalOfferPrices",
			match: func(r tool.Resource) bool { return r.Str("offerCode") == in.ID || r.Str("name") == in.Name },
		}
	case offerKindWinBack:
		if err := offerFirstErr(need("name", in.Name), need("identifier", in.ID), need("start_date", in.StartDate)); err != nil {
			return nil, sp, err
		}
		if in.PaidMonths < 1 || in.LapsedMinMonths < 1 || in.LapsedMaxMonths < in.LapsedMinMonths {
			return nil, sp, s12aInvalid("win-back needs paid_months ≥ 1 and 1 ≤ lapsed_min_months ≤ lapsed_max_months")
		}
		prio := in.Priority
		if prio == "" {
			prio = "NORMAL"
		}
		attrs["referenceName"], attrs["offerId"], attrs["periodCount"], attrs["priority"] = in.Name, in.ID, periods, prio
		attrs["customerEligibilityPaidSubscriptionDurationInMonths"] = in.PaidMonths
		attrs["customerEligibilityTimeSinceLastSubscribedInMonths"] = map[string]any{"minimum": in.LapsedMinMonths, "maximum": in.LapsedMaxMonths}
		if in.WaitMonths != nil {
			attrs["customerEligibilityWaitBetweenOffersInMonths"] = *in.WaitMonths
		}
		sp = offerSpec{
			path: "/v1/winBackOffers", typ: "winBackOffers", priceType: "winBackOfferPrices",
			match: func(r tool.Resource) bool { return r.Str("offerId") == in.ID || r.Str("referenceName") == in.Name },
		}
	case offerKindCode:
		if err := offerFirstErr(need("name", in.Name), need("offer_eligibility", in.OfferEligibility)); err != nil {
			return nil, sp, err
		}
		if len(in.Customers) == 0 {
			return nil, sp, s12aInvalid("customer_eligibilities is required for an offer code (NEW, EXISTING, EXPIRED)")
		}
		if in.StartDate != "" || in.EndDate != "" {
			return nil, sp, s12aInvalid("offer codes have no start/end date; set expiration_date when generating codes")
		}
		delete(attrs, "startDate")
		delete(attrs, "endDate")
		attrs["name"], attrs["customerEligibilities"], attrs["offerEligibility"], attrs["numberOfPeriods"] = in.Name, in.Customers, in.OfferEligibility, periods
		if in.AutoRenew != nil {
			attrs["autoRenewEnabled"] = *in.AutoRenew
		}
		sp = offerSpec{
			path: "/v1/subscriptionOfferCodes", typ: "subscriptionOfferCodes", priceType: "subscriptionOfferCodePrices",
			match: func(r tool.Resource) bool { return r.Str("name") == in.Name },
		}
	default:
		return nil, sp, s12aInvalid("unknown kind %q", in.Kind)
	}
	return attrs, sp, nil
}

// errorsJoin returns the first non-nil error.
func offerFirstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// offerCreateIntros creates one introductory offer per territory, skipping
// territories that already have one.
func offerCreateIntros(ctx context.Context, env *tool.Env, in offersCreateIn, attrs map[string]any, out offersCreateOut) (*tool.Result, error) {
	prices, err := offerResolvePrices(ctx, env, out.Subscription.ID, in.Mode, in.Prices, in.Equalize)
	if err != nil {
		return nil, err
	}
	existing, _, err := offerFetch(ctx, env, out.Subscription.ID, offerKindIntro)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, r := range existing {
		has[r.RelID("territory")] = true
	}
	todo := make([]offerPrice, 0, len(prices))
	for _, p := range prices {
		if has[p.Territory] {
			out.Skipped = append(out.Skipped, p.Territory)
			continue
		}
		todo = append(todo, p)
	}
	out.Territories = len(prices)
	if len(todo) == 0 {
		rows, err := offerListKind(ctx, env, out.Subscription.ID, offerKindIntro)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out.Offer = &rows[0]
		}
		return tool.Already(out), nil
	}
	if rl := env.ASC.RateLimit(); rl.Known && rl.Remaining < len(todo)+tool.RateLimitWarnBelow {
		return nil, fmt.Errorf("%w: %d intro offers need %d requests, %d remain this hour", tool.ErrRateLimited, len(todo), len(todo), rl.Remaining)
	}
	wctx, gres := offerConfirm(ctx, env, in, out.Subscription, attrs, todo)
	if gres != nil {
		return gres, nil
	}
	for _, p := range todo {
		rels := map[string]any{
			"subscription": offerOne("subscriptions", out.Subscription.ID),
			"territory":    offerOne("territories", p.Territory),
		}
		if p.PricePoint != "" {
			rels["subscriptionPricePoint"] = offerOne("subscriptionPricePoints", p.PricePoint)
		}
		r, err := offerPostDoc(wctx, env, "/v1/subscriptionIntroductoryOffers", "subscriptionIntroductoryOffers",
			offerDoc("subscriptionIntroductoryOffers", attrs, rels, nil))
		if err != nil {
			if s12aConflict(err) {
				out.Skipped = append(out.Skipped, p.Territory)
				continue
			}
			return nil, tool.WithNext(fmt.Errorf("created %d of %d intro offers, then %s failed: %w", out.Created, len(todo), p.Territory, err),
				tool.NextStep{Tool: "offers_list", Args: map[string]any{"product_id": in.ProductID}})
		}
		out.Created++
		if len(out.IDs) < offerShowIDs {
			out.IDs = append(out.IDs, r.ID)
		}
	}
	if len(prices) <= offerShowIDs {
		out.Prices = prices
	}
	return tool.OK(out), nil
}
