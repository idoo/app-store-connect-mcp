package monetization

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// availIn is the wanted sale availability of an IAP or subscription.
type availIn struct {
	Territories []string `json:"territories,omitempty" jsonschema:"3-letter territory codes; replaces the current list"`
	All         bool     `json:"all,omitempty" jsonschema:"every App Store territory"`
	NewTerrs    *bool    `json:"available_in_new_territories,omitempty" jsonschema:"default: current value, else all"`
}

// availKind describes where a product's availability lives.
type availKind struct {
	GetPath    string // availability of a product, with %s for the product id
	CreatePath string
	Type       string
	ParentRel  string
	ParentType string
}

var (
	iapAvail = availKind{
		GetPath: "/v2/inAppPurchases/%s/inAppPurchaseAvailability", CreatePath: "/v1/inAppPurchaseAvailabilities",
		Type: "inAppPurchaseAvailabilities", ParentRel: "inAppPurchase", ParentType: "inAppPurchases",
	}
	subAvail = availKind{
		GetPath: "/v1/subscriptions/%s/subscriptionAvailability", CreatePath: "/v1/subscriptionAvailabilities",
		Type: "subscriptionAvailabilities", ParentRel: "subscription", ParentType: "subscriptions",
	}
)

// availPlan is a planned availability write.
type availPlan struct {
	Territories []string `json:"territories"`
	NewTerrs    bool     `json:"available_in_new_territories"`
	Added       []string `json:"added,omitempty"`
	Removed     []string `json:"removed,omitempty"`
	Replace     bool     `json:"replace,omitempty"`
}

// checkAvail validates availability arguments before any request.
func checkAvail(in *availIn) error {
	if in == nil {
		return nil
	}
	if in.All == (len(in.Territories) > 0) {
		return invalidArgs("availability: give either territories or all:true")
	}
	_, err := prcTerritories("availability.territories", in.Territories)
	return err
}

// planAvail diffs the wanted availability against the product's current one
// (productID "" means the product does not exist yet). It returns nil when
// nothing changes.
func planAvail(ctx context.Context, env *tool.Env, k availKind, productID string, in *availIn) (*availPlan, error) {
	if in == nil {
		return nil, nil
	}
	want, err := prcTerritories("availability.territories", in.Territories)
	if err != nil {
		return nil, err
	}
	if in.All {
		all, err := prcAllTerritories(ctx, env)
		if err != nil {
			return nil, err
		}
		want = sortedKeys(all)
	}
	slices.Sort(want)
	want = slices.Compact(want)
	p := &availPlan{Territories: want, NewTerrs: in.All}
	if productID == "" {
		if in.NewTerrs != nil {
			p.NewTerrs = *in.NewTerrs
		}
		p.Added = want
		return p, nil
	}
	resp, err := env.ASC.Get(ctx, fmt.Sprintf(k.GetPath, url.PathEscape(productID)), url.Values{
		"fields[" + k.Type + "]": {"availableInNewTerritories"},
	})
	if err != nil && !tool.IsNotFound(err) {
		return nil, fmt.Errorf("read availability of %s: %w", productID, err)
	}
	cur, ok := tool.Resource{}, false
	if err == nil {
		cur, ok = resp.First()
	}
	if !ok || cur.ID == "" {
		if in.NewTerrs != nil {
			p.NewTerrs = *in.NewTerrs
		}
		p.Added = want
		return p, nil
	}
	curNew, _ := cur.Bool("availableInNewTerritories")
	p.NewTerrs = curNew
	if in.NewTerrs != nil {
		p.NewTerrs = *in.NewTerrs
	}
	terrs, err := env.ASC.List(ctx, "/v1/"+k.Type+"/"+url.PathEscape(cur.ID)+"/availableTerritories", tool.ListOptions{
		Fields: map[string][]string{"territories": {"currency"}}, Limit: 200, MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("read available territories of %s: %w", productID, err)
	}
	have := make([]string, 0, len(terrs.Data))
	for _, t := range terrs.Data {
		have = append(have, t.ID)
	}
	for _, t := range want {
		if !slices.Contains(have, t) {
			p.Added = append(p.Added, t)
		}
	}
	for _, t := range have {
		if !slices.Contains(want, t) {
			p.Removed = append(p.Removed, t)
		}
	}
	slices.Sort(p.Removed)
	if len(p.Added) == 0 && len(p.Removed) == 0 && curNew == p.NewTerrs {
		return nil, nil
	}
	p.Replace = true
	return p, nil
}

// gate adds the planned availability write to g: a first availability is
// additive, replacing one needs confirm.
func (p *availPlan) gate(g *upsertGate, k availKind) {
	if p == nil {
		return
	}
	level := gateAdditive
	desc := fmt.Sprintf("POST %s %d territories, new territories %t", k.CreatePath, len(p.Territories), p.NewTerrs)
	if p.Replace {
		level = gateOverwrite
		desc += fmt.Sprintf(" (adds %s; removes %s)", joinOrNone(p.Added), joinOrNone(p.Removed))
	}
	g.add(level, desc)
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

// applyAvail POSTs the planned availability of a product.
func applyAvail(ctx context.Context, env *tool.Env, k availKind, productID string, p *availPlan) upsertItem {
	it := upsertItem{Kind: k.Type, Action: actionCreated}
	if p.Replace {
		it.Action = actionUpdated
	}
	terrs := make([]any, 0, len(p.Territories))
	for _, t := range p.Territories {
		terrs = append(terrs, map[string]any{"type": "territories", "id": t})
	}
	body := map[string]any{"data": map[string]any{
		"type":       k.Type,
		"attributes": map[string]any{"availableInNewTerritories": p.NewTerrs},
		"relationships": map[string]any{
			k.ParentRel:            map[string]any{"data": map[string]any{"type": k.ParentType, "id": productID}},
			"availableTerritories": map[string]any{"data": terrs},
		},
	}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: k.CreatePath, Write: true, Body: body})
	if err != nil {
		it.Action, it.Error = actionFailed, err.Error()
		return it
	}
	if r, ok := resp.First(); ok {
		it.ID = r.ID
	}
	it.Note = fmt.Sprintf("%d territories", len(p.Territories))
	return it
}
