package monetization

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// Price kinds accepted by the pricing tools.
const (
	prcKindApp  = "app"
	prcKindIAP  = "iap"
	prcKindSub  = "subscription"
	prcDefaults = "USA"
	// prcPointsMax caps price points read per territory (Apple has ~900).
	prcPointsMax = 2000
	// prcPricesMax caps price rows read per schedule or subscription.
	prcPricesMax = 5000
)

// prcKind describes where one product family keeps its prices.
type prcKind struct {
	name string
	// schedule is the GET path of the current price schedule (app, IAP).
	schedule     func(id string) string
	scheduleType string
	schedulesAt  string // collection path for the schedule's manual/automatic prices
	createPath   string // POST path that replaces the schedule
	targetRel    string // schedule relationship to the product
	targetType   string
	priceType    string // inline price resource type
	priceRel     string // price → price point relationship
	pointType    string
	points       func(id string) string
	equalize     func(pointID string) string
	pointsLimit  int
}

var (
	prcApp = &prcKind{
		name:         prcKindApp,
		schedule:     func(id string) string { return "/v1/apps/" + url.PathEscape(id) + "/appPriceSchedule" },
		scheduleType: "appPriceSchedules",
		schedulesAt:  "/v1/appPriceSchedules/",
		createPath:   "/v1/appPriceSchedules",
		targetRel:    "app",
		targetType:   "apps",
		priceType:    "appPrices",
		priceRel:     "appPricePoint",
		pointType:    "appPricePoints",
		points:       func(id string) string { return "/v1/apps/" + url.PathEscape(id) + "/appPricePoints" },
		equalize:     func(p string) string { return "/v3/appPricePoints/" + url.PathEscape(p) + "/equalizations" },
		pointsLimit:  200,
	}
	prcIAP = &prcKind{
		name:         prcKindIAP,
		schedule:     func(id string) string { return "/v2/inAppPurchases/" + url.PathEscape(id) + "/iapPriceSchedule" },
		scheduleType: "inAppPurchasePriceSchedules",
		schedulesAt:  "/v1/inAppPurchasePriceSchedules/",
		createPath:   "/v1/inAppPurchasePriceSchedules",
		targetRel:    "inAppPurchase",
		targetType:   "inAppPurchases",
		priceType:    "inAppPurchasePrices",
		priceRel:     "inAppPurchasePricePoint",
		pointType:    "inAppPurchasePricePoints",
		points:       func(id string) string { return "/v2/inAppPurchases/" + url.PathEscape(id) + "/pricePoints" },
		equalize:     func(p string) string { return "/v1/inAppPurchasePricePoints/" + url.PathEscape(p) + "/equalizations" },
		pointsLimit:  1000,
	}
	prcSub = &prcKind{
		name:        prcKindSub,
		createPath:  "/v1/subscriptionPrices",
		targetRel:   "subscription",
		targetType:  "subscriptions",
		priceType:   "subscriptionPrices",
		priceRel:    "subscriptionPricePoint",
		pointType:   "subscriptionPricePoints",
		points:      func(id string) string { return "/v1/subscriptions/" + url.PathEscape(id) + "/pricePoints" },
		equalize:    func(p string) string { return "/v1/subscriptionPricePoints/" + url.PathEscape(p) + "/equalizations" },
		pointsLimit: 1000,
	}
)

// prcKindFor maps the kind argument to its descriptor.
func prcKindFor(kind string) (*prcKind, error) {
	switch kind {
	case "", prcKindApp:
		return prcApp, nil
	case prcKindIAP:
		return prcIAP, nil
	case prcKindSub:
		return prcSub, nil
	}
	return nil, invalidArgs("kind must be app, iap or subscription, got %q", kind)
}

// prcTarget is the resolved product whose prices are read or changed.
type prcTarget struct {
	Kind      string `json:"kind"`
	AppID     string `json:"app_id"`
	AppName   string `json:"app_name,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	ID        string `json:"id"`
}

// prcResolve resolves the app and, for iap/subscription, the product id.
func prcResolve(ctx context.Context, env *tool.Env, ref tool.AppRef, k *prcKind, productID string) (prcTarget, error) {
	app, err := env.Resolve.App(ctx, ref)
	if err != nil {
		return prcTarget{}, err
	}
	t := prcTarget{Kind: k.name, AppID: app.ID, AppName: app.Name, ProductID: productID}
	if k == prcApp {
		if productID != "" {
			return t, invalidArgs("product_id applies to kind iap or subscription only")
		}
		t.ID = app.ID
		return t, nil
	}
	if err := checkProductID(productID); err != nil {
		return t, err
	}
	if k == prcIAP {
		p, _, found, err := iapFind(ctx, env, app.ID, productID)
		if err != nil {
			return t, err
		}
		if !found {
			return t, &tool.ErrNotFound{Kind: "in-app purchase", Ref: productID, Next: []tool.NextStep{{Tool: "iap_list", Args: map[string]any{"app": app.ID}}}}
		}
		t.ID = p.ID
		return t, nil
	}
	id, err := prcFindSub(ctx, env, app.ID, productID)
	if err != nil {
		return t, err
	}
	if id == "" {
		return t, &tool.ErrNotFound{Kind: "subscription", Ref: productID, Next: []tool.NextStep{{Tool: "subscriptions_list", Args: map[string]any{"app": app.ID}}}}
	}
	t.ID = id
	return t, nil
}

// prcFindSub returns the id of the app's subscription with productID, or "".
func prcFindSub(ctx context.Context, env *tool.Env, appID, productID string) (string, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/subscriptionGroups", tool.ListOptions{
		Query:   url.Values{"limit[subscriptions]": {"50"}},
		Include: []string{"subscriptions"},
		Fields: map[string][]string{
			"subscriptionGroups": {"subscriptions"},
			"subscriptions":      {"productId"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("look up subscription %s: %w", productID, err)
	}
	inc := indexIncluded(res.Included)
	for _, g := range res.Data {
		for _, s := range related(g, "subscriptions", "subscriptions", inc) {
			if s.Str("productId") == productID {
				return s.ID, nil
			}
		}
	}
	return "", nil
}

// prcPoint is one price point in one territory.
type prcPoint struct {
	ID            string `json:"price_point_id"`
	Territory     string `json:"territory"`
	Currency      string `json:"currency,omitempty"`
	CustomerPrice string `json:"customer_price"`
	Proceeds      string `json:"proceeds,omitempty"`
}

// prcPrice is one scheduled price (manual or automatic) of a territory.
type prcPrice struct {
	prcPoint
	Manual    bool   `json:"manual,omitempty"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
	PlanType  string `json:"plan_type,omitempty"`
	Preserved bool   `json:"preserved,omitempty"`
}

// prcCurrencies indexes territory currencies from included territories.
func prcCurrencies(inc []tool.Resource) map[string]string {
	m := map[string]string{}
	for _, r := range inc {
		if r.Type == "territories" {
			m[r.ID] = r.Str("currency")
		}
	}
	return m
}

// prcPointOf flattens a price point resource.
func prcPointOf(r tool.Resource, cur map[string]string) prcPoint {
	terr := r.RelID("territory")
	return prcPoint{ID: r.ID, Territory: terr, Currency: cur[terr], CustomerPrice: r.Str("customerPrice"), Proceeds: r.Str("proceeds")}
}

// prcPointQuery is the shared query of price point collections.
func prcPointOpts(k *prcKind, q url.Values) tool.ListOptions {
	return tool.ListOptions{
		Query:    q,
		Include:  []string{"territory"},
		Fields:   map[string][]string{k.pointType: {"customerPrice", "proceeds", "territory"}, "territories": {"currency"}},
		Limit:    k.pointsLimit,
		MaxItems: prcPointsMax,
	}
}

// prcPoints lists a product's price points in one territory.
func prcPoints(ctx context.Context, env *tool.Env, k *prcKind, id, territory, planType string) ([]prcPoint, error) {
	q := url.Values{"filter[territory]": {territory}}
	if k == prcSub && planType != "" {
		q.Set("filter[planType]", planType)
	}
	res, err := env.ASC.List(ctx, k.points(id), prcPointOpts(k, q))
	if err != nil {
		return nil, fmt.Errorf("list %s price points in %s: %w", k.name, territory, err)
	}
	cur := prcCurrencies(res.Included)
	out := make([]prcPoint, 0, len(res.Data))
	for _, r := range res.Data {
		p := prcPointOf(r, cur)
		if p.Territory == "" {
			p.Territory = territory
		}
		out = append(out, p)
	}
	return out, nil
}

// prcRat parses a decimal price.
func prcRat(s string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	return r, ok
}

// prcMatch picks the price point equal to price, else the nearest (ties go
// to the lower price). exact reports an exact match.
func prcMatch(points []prcPoint, price, territory string) (best prcPoint, exact bool, err error) {
	want, ok := prcRat(price)
	if !ok || want.Sign() < 0 {
		return best, false, invalidArgs("price %q for %s is not a decimal customer price", price, territory)
	}
	var bestDiff, bestVal *big.Rat
	for _, p := range points {
		v, ok := prcRat(p.CustomerPrice)
		if !ok {
			continue
		}
		d := new(big.Rat).Sub(v, want)
		d.Abs(d)
		if bestDiff == nil || d.Cmp(bestDiff) < 0 || (d.Cmp(bestDiff) == 0 && v.Cmp(bestVal) < 0) {
			best, bestDiff, bestVal = p, d, v
		}
	}
	if bestDiff == nil {
		return best, false, invalidArgs("no price points in territory %s", territory)
	}
	return best, bestDiff.Sign() == 0, nil
}

// prcEqualizations returns Apple's equalized price points for every other
// territory, keyed by territory.
func prcEqualizations(ctx context.Context, env *tool.Env, k *prcKind, pointID, planType string) (map[string]prcPoint, error) {
	q := url.Values{}
	if k == prcSub && planType != "" {
		q.Set("filter[planType]", planType)
	}
	opts := prcPointOpts(k, q)
	opts.Limit = 200
	res, err := env.ASC.List(ctx, k.equalize(pointID), opts)
	if err != nil {
		return nil, fmt.Errorf("list equalized prices: %w", err)
	}
	cur := prcCurrencies(res.Included)
	out := make(map[string]prcPoint, len(res.Data))
	for _, r := range res.Data {
		p := prcPointOf(r, cur)
		if p.Territory != "" {
			out[p.Territory] = p
		}
	}
	return out, nil
}

// prcSchedule is the current price schedule of an app or IAP.
type prcSchedule struct {
	ID        string     `json:"id,omitempty"`
	Base      string     `json:"base_territory,omitempty"`
	Manual    []prcPrice `json:"manual"`
	Automatic []prcPrice `json:"automatic"`
}

// prcReadSchedule reads an app/IAP schedule with all manual prices and the
// automatic prices of territories (all when empty). A missing schedule
// (404) yields a zero schedule.
func prcReadSchedule(ctx context.Context, env *tool.Env, k *prcKind, id string, territories []string) (prcSchedule, error) {
	var s prcSchedule
	resp, err := env.ASC.Get(ctx, k.schedule(id), url.Values{
		"include":                        {"baseTerritory"},
		"fields[" + k.scheduleType + "]": {"baseTerritory"},
		"fields[territories]":            {"currency"},
	})
	if prcNotFound(err) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read %s price schedule: %w", k.name, err)
	}
	r, ok := resp.First()
	if !ok {
		return s, nil
	}
	s.ID, s.Base = r.ID, r.RelID("baseTerritory")
	if s.Manual, err = prcSchedulePrices(ctx, env, k, s.ID, "manualPrices", nil); err != nil {
		return s, err
	}
	if s.Automatic, err = prcSchedulePrices(ctx, env, k, s.ID, "automaticPrices", territories); err != nil {
		return s, err
	}
	return s, nil
}

// prcSchedulePrices lists one price relationship of a schedule.
func prcSchedulePrices(ctx context.Context, env *tool.Env, k *prcKind, scheduleID, rel string, territories []string) ([]prcPrice, error) {
	q := url.Values{}
	if len(territories) > 0 {
		q.Set("filter[territory]", strings.Join(territories, ","))
	}
	res, err := env.ASC.List(ctx, k.schedulesAt+url.PathEscape(scheduleID)+"/"+rel, tool.ListOptions{
		Query:   q,
		Include: []string{k.priceRel, "territory"},
		Fields: map[string][]string{
			k.priceType:   {"manual", "startDate", "endDate", k.priceRel, "territory"},
			k.pointType:   {"customerPrice", "proceeds", "territory"},
			"territories": {"currency"},
		},
		Limit:    200,
		MaxItems: prcPricesMax,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s %s: %w", k.name, rel, err)
	}
	return prcPricesOf(k, res, rel == "manualPrices"), nil
}

// prcPricesOf flattens price rows with their included price points.
func prcPricesOf(k *prcKind, res *tool.ListResult, manual bool) []prcPrice {
	inc := indexIncluded(res.Included)
	cur := prcCurrencies(res.Included)
	out := make([]prcPrice, 0, len(res.Data))
	for _, r := range res.Data {
		p := prcPrice{Manual: manual, StartDate: r.Str("startDate"), EndDate: r.Str("endDate"), PlanType: r.Str("planType")}
		p.Preserved, _ = r.Bool("preserved")
		if m, ok := r.Bool("manual"); ok {
			p.Manual = m
		}
		pid := r.RelID(k.priceRel)
		if pt, ok := inc[k.pointType+"/"+pid]; ok {
			p.prcPoint = prcPointOf(pt, cur)
		}
		p.ID = pid
		if t := r.RelID("territory"); t != "" {
			p.Territory = t
		}
		p.Currency = cur[p.Territory]
		out = append(out, p)
	}
	return out
}

// prcSubPrices lists a subscription's price rows (past, current, future).
func prcSubPrices(ctx context.Context, env *tool.Env, id string, territories []string, planType string) ([]prcPrice, error) {
	q := url.Values{}
	if len(territories) > 0 {
		q.Set("filter[territory]", strings.Join(territories, ","))
	}
	if planType != "" {
		q.Set("filter[planType]", planType)
	}
	res, err := env.ASC.List(ctx, "/v1/subscriptions/"+url.PathEscape(id)+"/prices", tool.ListOptions{
		Query:   q,
		Include: []string{"territory", "subscriptionPricePoint"},
		Fields: map[string][]string{
			"subscriptionPrices":      {"startDate", "preserved", "planType", "territory", "subscriptionPricePoint"},
			"subscriptionPricePoints": {"customerPrice", "proceeds", "territory"},
			"territories":             {"currency"},
		},
		Limit:    200,
		MaxItems: prcPricesMax,
	})
	if err != nil {
		return nil, fmt.Errorf("list subscription prices: %w", err)
	}
	return prcPricesOf(prcSub, res, false), nil
}

// prcEffective returns, per territory, the price in effect on date
// (YYYY-MM-DD): the latest-starting row that has started and not ended.
// Manual rows win over automatic ones.
func prcEffective(rows []prcPrice, date string) map[string]prcPrice {
	out := map[string]prcPrice{}
	for _, p := range rows {
		if p.Territory == "" || (p.StartDate != "" && p.StartDate > date) || (p.EndDate != "" && p.EndDate <= date) {
			continue
		}
		old, ok := out[p.Territory]
		switch {
		case !ok, p.Manual && !old.Manual:
			out[p.Territory] = p
		case p.Manual == old.Manual && p.StartDate > old.StartDate:
			out[p.Territory] = p
		}
	}
	return out
}

// prcToday is the current date in Apple's Pacific business day.
func prcToday(env *tool.Env) string {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.UTC
	}
	return env.Clock.Now().In(loc).Format(time.DateOnly)
}

// prcTerritoryRE is an ISO 3166-1 alpha-3 territory code.
var prcTerritoryRE = regexp.MustCompile(`^[A-Z]{3}$`)

// prcTerritories normalizes and validates territory codes (deduplicated,
// sorted).
func prcTerritories(arg string, in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToUpper(strings.TrimSpace(t))
		if !prcTerritoryRE.MatchString(t) {
			return nil, invalidArgs("%s: %q is not a 3-letter territory code (e.g. USA, SWE)", arg, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// prcTop lists major storefronts shown first when no territories are asked.
var prcTop = []string{
	"USA", "CHN", "JPN", "GBR", "DEU", "FRA", "KOR", "CAN", "AUS", "ITA",
	"ESP", "NLD", "CHE", "SWE", "BRA", "MEX", "IND", "TUR", "NOR", "DNK",
}

// prcLimit orders prices by prcTop then territory and keeps n.
func prcLimit(m map[string]prcPrice, n int) []prcPrice {
	out := make([]prcPrice, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	rank := func(t string) int {
		if i := slices.Index(prcTop, t); i >= 0 {
			return i
		}
		return len(prcTop)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank(out[i].Territory), rank(out[j].Territory)
		if ri != rj {
			return ri < rj
		}
		return out[i].Territory < out[j].Territory
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// prcNotFound reports an Apple 404.
func prcNotFound(err error) bool {
	var api *tool.APIError
	return errors.As(err, &api) && api.Status == 404
}

// prcUnknown reports a mutation failure whose outcome Apple may have
// applied: transport errors, timeouts and 5xx.
func prcUnknown(err error) bool {
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		return true
	}
	var api *tool.APIError
	if errors.As(err, &api) {
		return api.Status >= 500
	}
	var te *tool.Error
	return !errors.As(err, &te)
}

// prcRel is a to-one JSON:API relationship value.
func prcRel(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}
