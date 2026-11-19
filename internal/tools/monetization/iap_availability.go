package monetization

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// availIn is the wanted sale availability of an IAP or subscription.
type availIn struct {
	Territories    []string `json:"territories,omitempty" jsonschema:"3-letter territory codes; replaces the current list"`
	All            bool     `json:"all,omitempty" jsonschema:"every App Store territory"`
	RemoveFromSale bool     `json:"remove_from_sale,omitempty" jsonschema:"no territory: stop selling it everywhere (Remove from Sale)"`
	NewTerrs       *bool    `json:"available_in_new_territories,omitempty" jsonschema:"default: current value, else all"`
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
	// RemoveFromSale marks a full removal: no territory, no new territories.
	RemoveFromSale bool `json:"remove_from_sale,omitempty"`
}

// checkAvail validates availability arguments before any request.
func checkAvail(in *availIn) error {
	if in == nil {
		return nil
	}
	n := 0
	for _, set := range []bool{len(in.Territories) > 0, in.All, in.RemoveFromSale} {
		if set {
			n++
		}
	}
	if n != 1 {
		return tool.InvalidArgs("availability: give exactly one of territories, all:true or remove_from_sale:true")
	}
	if in.RemoveFromSale && in.NewTerrs != nil && *in.NewTerrs {
		return tool.InvalidArgs("availability: remove_from_sale also stops new territories; drop available_in_new_territories:true")
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
		want = slices.Sorted(maps.Keys(all))
	}
	slices.Sort(want)
	want = slices.Compact(want)
	p := &availPlan{Territories: want, NewTerrs: in.All, RemoveFromSale: in.RemoveFromSale}
	if productID == "" {
		if in.RemoveFromSale {
			return nil, tool.InvalidArgs("availability.remove_from_sale: the product does not exist yet, so nothing is on sale")
		}
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
		if in.RemoveFromSale {
			return nil, nil // never had an availability: not on sale anywhere
		}
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
	if in.RemoveFromSale {
		p.NewTerrs = false
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
// additive, replacing one needs confirm, and removing territories stops sales
// there, so it needs human approval. A full removal is described by its count:
// listing every territory code would overflow the approval prompt.
func (p *availPlan) gate(g *upsertGate, k availKind) {
	if p == nil {
		return
	}
	if p.RemoveFromSale {
		n := len(p.Removed)
		g.add(gateHuman, fmt.Sprintf("POST %s 0 territories, new territories false: removes it from sale in all %d territories", k.CreatePath, n))
		if k.Type == subAvail.Type {
			g.irreversible(fmt.Sprintf("discontinues it as an auto-renewable subscription in all %d territories: existing subscribers cannot renew, "+
				"and you must give eligible subscribers full service until the end of their current billing period", n))
		} else {
			g.irreversible(fmt.Sprintf("it stops being sold in all %d territories", n))
		}
		return
	}
	level := gateAdditive
	desc := fmt.Sprintf("POST %s %d territories, new territories %t", k.CreatePath, len(p.Territories), p.NewTerrs)
	if p.Replace {
		level = gateOverwrite
		desc += fmt.Sprintf(" (adds %s; removes %s)", joinOrNone(p.Added), joinOrNone(p.Removed))
	}
	if len(p.Removed) > 0 {
		level = gateHuman
	}
	g.add(level, desc)
	if len(p.Removed) > 0 {
		g.irreversible("stops sales in " + joinOrNone(p.Removed))
	}
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

// accountHolderOnlyCode is Apple's 409 code when an API key tries to remove
// availability of an on-sale subscription.
const accountHolderOnlyCode = "STATE_ERROR.INVALID_REQUEST_SUBSCRIPTION_AVAILABILITY_REMOVAL_NOT_ALLOWED"

// accountHolderOnly reports Apple's 409 refusal that only the Account Holder
// may change the availability of an on-sale product.
func accountHolderOnly(err error) bool {
	var ae *tool.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		return false
	}
	return ae.Code == accountHolderOnlyCode || strings.Contains(strings.ToLower(ae.Detail), "nly account holder")
}

// applyAvail POSTs the planned availability of a product. When Apple allows
// the change only to the Account Holder, the identical request is sent once
// through the web session (the App Store Connect page does the same).
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
	req := tool.Request{Method: http.MethodPost, Path: k.CreatePath, Write: true, Body: body}
	via := ""
	resp, err := env.ASC.Do(ctx, req)
	if err != nil && accountHolderOnly(err) {
		var werr error
		if resp, werr = env.Web.Do(ctx, req); werr == nil {
			via = ", via web session (Account Holder only)"
		}
		err = accountHolderErr(err, werr, p.RemoveFromSale)
	}
	if err != nil {
		it.fail(err)
		return it
	}
	if r, ok := resp.First(); ok {
		it.ID = r.ID
	}
	it.Note = fmt.Sprintf("%d territories%s", len(p.Territories), via)
	return it
}

// accountHolderErr is the outcome of retrying an Account-Holder-only
// availability change (apiErr from the API key) through the web session
// (webErr, nil when it worked).
func accountHolderErr(apiErr, webErr error, removal bool) error {
	where := "change its availability"
	if removal {
		where = "use Remove from Sale"
	}
	switch {
	case webErr == nil:
		return nil
	case errors.Is(webErr, tool.ErrWebSessionRequired):
		return tool.WithNext(fmt.Errorf("%w; Apple lets only the Account Holder change the availability of this on-sale product and the API key cannot: "+
			"sign in as the Account Holder (call web_login, web toolset) and call again, or %s on the product's App Store Connect page (%w)",
			apiErr, where, webErr), tool.NextStep{Tool: "web_login"})
	case accountHolderOnly(webErr):
		return tool.WithNext(fmt.Errorf("%w; the signed-in Apple ID is not the Account Holder either: only the Account Holder can change the availability "+
			"of this on-sale product; sign in as the Account Holder (web_login) and call again, or %s on the product's App Store Connect page",
			webErr, where), tool.NextStep{Tool: "web_login"})
	default:
		return fmt.Errorf("retry through the web session (the API key may not change this on-sale product): %w", webErr)
	}
}
