package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/idoo/hamn/internal/tool"
)

// prcGetMaxProducts caps the products of one pricing_get call.
const prcGetMaxProducts = 10

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "pricing_get", Toolset: tool.Monetization, Class: tool.Read, Idempotent: true,
		Title: "Get current prices",
		Description: "Current prices of an app, IAP or subscription: base territory, prices in effect per territory " +
			"(requested or top 20, with total count), scheduled changes, app availability. products (≤10 ids or " +
			"product ids) returns each one's base price and territory count.",
		Enums: map[string][]any{"kind": prcKinds},
	}, pricingGet))
}

type pricingGetIn struct {
	App         tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Kind        string      `json:"kind,omitempty" jsonschema:"default app"`
	ProductID   string      `json:"product_id,omitempty" jsonschema:"IAP or subscription: product id or the id from iap_list/subscriptions_list"`
	Products    []string    `json:"products,omitempty" jsonschema:"up to 10 product ids or ids of kind iap/subscription; compact summary per product"`
	Territories []string    `json:"territories,omitempty" jsonschema:"3-letter codes to show; default: top 20 storefronts"`
	ForPlan     bool        `json:"for_plan,omitempty" jsonschema:"include price_point_id per row"`
}

// prcGetRow is one price row of pricing_get; PointID only with for_plan.
type prcGetRow struct {
	Territory string `json:"territory"`
	Currency  string `json:"currency,omitempty"`
	Price     string `json:"customer_price"`
	Proceeds  string `json:"proceeds,omitempty"`
	Manual    bool   `json:"manual,omitempty"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
	PlanType  string `json:"plan_type,omitempty"`
	Preserved bool   `json:"preserved,omitempty"`
	PointID   string `json:"price_point_id,omitempty"`
}

type pricingGetOut struct {
	Target         prcTarget        `json:"target"`
	AsOf           string           `json:"as_of"`
	ScheduleID     string           `json:"schedule_id,omitempty"`
	BaseTerritory  string           `json:"base_territory,omitempty"`
	Prices         []prcGetRow      `json:"prices"`
	TerritoryCount int              `json:"territory_count"`
	Scheduled      []prcGetRow      `json:"scheduled,omitempty"`
	Availability   *prcAvailSummary `json:"availability,omitempty"`
	Note           string           `json:"note,omitempty"`
}

// prcProductPrice is the compact per-product summary of products mode.
type prcProductPrice struct {
	ID             string `json:"id,omitempty"`
	ProductID      string `json:"product_id,omitempty"`
	Ref            string `json:"ref,omitempty"`
	Territory      string `json:"territory,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Price          string `json:"customer_price,omitempty"`
	TerritoryCount int    `json:"territory_count"`
	Scheduled      int    `json:"scheduled_changes,omitempty"`
	Error          string `json:"error,omitempty"`
}

type pricingGetProductsOut struct {
	Kind     string            `json:"kind"`
	AppID    string            `json:"app_id"`
	AsOf     string            `json:"as_of"`
	Products []prcProductPrice `json:"products"`
	Note     string            `json:"note,omitempty"`
}

func pricingGet(ctx context.Context, env *tool.Env, in pricingGetIn) (*tool.Result, error) {
	k, err := prcKindFor(in.Kind)
	if err != nil {
		return nil, err
	}
	terrs, err := prcTerritories("territories", in.Territories)
	if err != nil {
		return nil, err
	}
	if len(in.Products) > 0 {
		return pricingGetProducts(ctx, env, in, k, terrs)
	}
	t, err := prcGetResolve(ctx, env, in.App, k, in.ProductID)
	if err != nil {
		return nil, err
	}
	today := prcToday(env)
	out := pricingGetOut{Target: t, AsOf: today}
	scheduleID, base, rows, err := prcGetRows(ctx, env, k, t.ID, terrs)
	if err != nil {
		return nil, err
	}
	out.ScheduleID, out.BaseTerritory = scheduleID, base
	eff := prcEffective(rows, today)
	out.TerritoryCount = len(eff)
	for _, p := range prcLimit(eff, len(prcTop)) {
		out.Prices = append(out.Prices, prcGetRowOf(p, in.ForPlan))
	}
	if out.Prices == nil {
		out.Prices = []prcGetRow{}
	}
	for _, r := range rows {
		if r.StartDate > today {
			out.Scheduled = append(out.Scheduled, prcGetRowOf(r, in.ForPlan))
		}
	}
	if k == prcApp {
		a, err := prcReadAvail(ctx, env, t.AppID)
		if err != nil {
			return nil, err
		}
		s := a.summary()
		out.Availability = &s
	}
	if out.TerritoryCount > len(out.Prices) {
		out.Note = fmt.Sprintf("showing %d of %d territories; pass territories to see others", len(out.Prices), out.TerritoryCount)
	}
	return tool.OK(out), nil
}

// pricingGetProducts summarizes up to prcGetMaxProducts products of one kind.
func pricingGetProducts(ctx context.Context, env *tool.Env, in pricingGetIn, k *prcKind, terrs []string) (*tool.Result, error) {
	if k == prcApp {
		return nil, invalidArgs("products needs kind iap or subscription")
	}
	if in.ProductID != "" {
		return nil, invalidArgs("pass product_id or products, not both")
	}
	if len(in.Products) > prcGetMaxProducts {
		return nil, invalidArgs("products: at most %d per call, got %d", prcGetMaxProducts, len(in.Products))
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	catalog, err := prcGetCatalog(ctx, env, app.ID, k)
	if err != nil {
		return nil, err
	}
	today := prcToday(env)
	out := pricingGetProductsOut{Kind: k.name, AppID: app.ID, AsOf: today, Products: make([]prcProductPrice, len(in.Products))}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, ref := range in.Products {
		p, ok := prcGetMatch(catalog, ref)
		if !ok {
			out.Products[i] = prcProductPrice{Ref: ref, Error: fmt.Sprintf("no %s with id or product id %q", k.name, ref)}
			continue
		}
		out.Products[i] = prcProductPrice{ID: p.ID, ProductID: p.ProductID}
		wg.Add(1)
		go func(row *prcProductPrice) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			prcGetSummarize(ctx, env, k, terrs, today, row)
		}(&out.Products[i])
	}
	wg.Wait()
	if len(terrs) == 0 {
		out.Note = "territory is the base territory (subscriptions: USA or the top storefront); pass product_id for per-territory prices"
	}
	return tool.OK(out), nil
}

// prcGetSummarize fills row with the base-territory price and counts.
func prcGetSummarize(ctx context.Context, env *tool.Env, k *prcKind, terrs []string, today string, row *prcProductPrice) {
	_, base, rows, err := prcGetRows(ctx, env, k, row.ID, terrs)
	if err != nil {
		row.Error = err.Error()
		return
	}
	eff := prcEffective(rows, today)
	row.TerritoryCount = len(eff)
	for _, r := range rows {
		if r.StartDate > today {
			row.Scheduled++
		}
	}
	p, ok := eff[base]
	if !ok {
		top := prcLimit(eff, 1)
		if len(top) == 0 {
			return
		}
		p = top[0]
	}
	row.Territory, row.Currency, row.Price = p.Territory, p.Currency, p.CustomerPrice
}

// prcGetRows reads all price rows of one product (schedule id and base
// territory for app/IAP).
func prcGetRows(ctx context.Context, env *tool.Env, k *prcKind, id string, terrs []string) (scheduleID, base string, rows []prcPrice, err error) {
	if k == prcSub {
		rows, err = prcSubPrices(ctx, env, id, terrs, "")
		return "", "", rows, err
	}
	s, err := prcReadSchedule(ctx, env, k, id, terrs)
	if err != nil {
		return "", "", nil, err
	}
	return s.ID, s.Base, append(append(rows, s.Manual...), s.Automatic...), nil
}

func prcGetRowOf(p prcPrice, withPoint bool) prcGetRow {
	r := prcGetRow{
		Territory: p.Territory, Currency: p.Currency, Price: p.CustomerPrice, Proceeds: p.Proceeds, Manual: p.Manual,
		StartDate: p.StartDate, EndDate: p.EndDate, PlanType: p.PlanType, Preserved: p.Preserved,
	}
	if withPoint {
		r.PointID = p.ID
	}
	return r
}

// prcProductRef is one IAP or subscription of an app.
type prcProductRef struct{ ID, ProductID string }

// prcGetResolve resolves the target of pricing_get; for iap/subscription ref
// may be the product id or the resource id from iap_list/subscriptions_list.
func prcGetResolve(ctx context.Context, env *tool.Env, appRef tool.AppRef, k *prcKind, ref string) (prcTarget, error) {
	if k == prcApp {
		return prcResolve(ctx, env, appRef, k, ref)
	}
	app, err := env.Resolve.App(ctx, appRef)
	if err != nil {
		return prcTarget{}, err
	}
	t := prcTarget{Kind: k.name, AppID: app.ID, AppName: app.Name}
	if ref == "" {
		return t, invalidArgs("product_id is required for kind %s", k.name)
	}
	catalog, err := prcGetCatalog(ctx, env, app.ID, k)
	if err != nil {
		return t, err
	}
	p, ok := prcGetMatch(catalog, ref)
	if !ok {
		next := "iap_list"
		if k == prcSub {
			next = "subscriptions_list"
		}
		return t, &tool.ErrNotFound{Kind: k.name, Ref: ref, Next: []tool.NextStep{{Tool: next, Args: map[string]any{"app": app.ID}}}}
	}
	t.ID, t.ProductID = p.ID, p.ProductID
	return t, nil
}

// prcGetMatch finds ref by product id or resource id.
func prcGetMatch(catalog []prcProductRef, ref string) (prcProductRef, bool) {
	i := slices.IndexFunc(catalog, func(p prcProductRef) bool { return p.ProductID == ref || p.ID == ref })
	if i < 0 {
		return prcProductRef{}, false
	}
	return catalog[i], true
}

// prcGetCatalog lists the app's IAPs or subscriptions (id + product id).
func prcGetCatalog(ctx context.Context, env *tool.Env, appID string, k *prcKind) ([]prcProductRef, error) {
	if k == prcIAP {
		res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/inAppPurchasesV2", tool.ListOptions{
			Fields: map[string][]string{"inAppPurchases": {"productId"}},
			Limit:  200,
		})
		if err != nil {
			return nil, fmt.Errorf("list in-app purchases: %w", err)
		}
		out := make([]prcProductRef, 0, len(res.Data))
		for _, r := range res.Data {
			out = append(out, prcProductRef{ID: r.ID, ProductID: r.Str("productId")})
		}
		return out, nil
	}
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/subscriptionGroups", tool.ListOptions{
		Query:   url.Values{"limit[subscriptions]": {"50"}},
		Include: []string{"subscriptions"},
		Fields: map[string][]string{
			"subscriptionGroups": {"subscriptions"},
			"subscriptions":      {"productId"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	inc := indexIncluded(res.Included)
	var out []prcProductRef
	for _, g := range res.Data {
		for _, s := range related(g, "subscriptions", "subscriptions", inc) {
			out = append(out, prcProductRef{ID: s.ID, ProductID: s.Str("productId")})
		}
	}
	return out, nil
}
