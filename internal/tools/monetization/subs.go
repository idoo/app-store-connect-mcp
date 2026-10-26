package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

var subsPeriods = []any{"ONE_WEEK", "ONE_MONTH", "TWO_MONTHS", "THREE_MONTHS", "SIX_MONTHS", "ONE_YEAR"}

// subsMaxGroups bounds the per-group detail requests of subscriptions_list.
const subsMaxGroups = 10

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "subscriptions_list", Toolset: tool.Monetization, Class: tool.Read, Idempotent: true,
		Title: "List subscriptions",
		Description: "Subscription groups with localizations, and each group's subscriptions: product id, period, level, " +
			"state, latest version state and offer counts. One request per group. Prices: pricing_get.",
	}, subsList))
	tool.Register(tool.New(tool.Meta{
		Name: "subscriptions_upsert", Toolset: tool.Monetization, Class: tool.Write, Idempotent: true, Confirm: tool.ConfirmByFlag,
		Title: "Create or update a subscription",
		Description: "Find or create a subscription group by reference name and a subscription by product_id; PATCH only " +
			"changed fields, plus localizations, review screenshot and availability. Overwrites need confirm:true; " +
			"creating, family sharing on and group level changes need a human.",
		Enums: map[string][]any{"period": subsPeriods},
	}, subsUpsert))
}

type subsListIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
}

type subsGroupLocOut struct {
	Locale        string `json:"locale"`
	Name          string `json:"name,omitempty"`
	CustomAppName string `json:"custom_app_name,omitempty"`
}

type subsOffersOut struct {
	Introductory int `json:"introductory"`
	Promotional  int `json:"promotional"`
	OfferCodes   int `json:"offer_codes"`
	WinBack      int `json:"win_back"`
}

type subsOut struct {
	ID             string         `json:"id"`
	ProductID      string         `json:"product_id"`
	Name           string         `json:"name"`
	Period         string         `json:"period,omitempty"`
	GroupLevel     int            `json:"group_level,omitempty"`
	State          string         `json:"state"`
	FamilySharable bool           `json:"family_sharable,omitempty"`
	Version        *iapVersionOut `json:"version,omitempty"`
	Offers         subsOffersOut  `json:"offers"`
}

type subsGroupOut struct {
	ID            string            `json:"id"`
	ReferenceName string            `json:"reference_name"`
	Localizations []subsGroupLocOut `json:"localizations,omitempty"`
	Subscriptions []subsOut         `json:"subscriptions"`
	Detail        string            `json:"detail,omitempty"`
}

type subsListOut struct {
	App           tool.App       `json:"app"`
	Groups        []subsGroupOut `json:"groups"`
	Subscriptions int            `json:"subscriptions"`
	ByState       map[string]int `json:"by_state"`
}

func subsList(ctx context.Context, env *tool.Env, in subsListIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	groups, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/subscriptionGroups", tool.ListOptions{
		Query:   url.Values{"limit[subscriptionGroupLocalizations]": {"50"}},
		Include: []string{"subscriptionGroupLocalizations"},
		Fields: map[string][]string{
			"subscriptionGroups":             {"referenceName", "subscriptionGroupLocalizations"},
			"subscriptionGroupLocalizations": {"locale", "name", "customAppName"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list subscription groups: %w", err)
	}
	ginc := indexIncluded(groups.Included)
	out := subsListOut{App: app, Groups: []subsGroupOut{}, ByState: map[string]int{}}
	res := tool.OK(&out)
	for i, g := range groups.Data {
		gout := subsGroupOut{ID: g.ID, ReferenceName: g.Str("referenceName"), Subscriptions: []subsOut{}}
		for _, l := range related(g, "subscriptionGroupLocalizations", "subscriptionGroupLocalizations", ginc) {
			gout.Localizations = append(gout.Localizations, subsGroupLocOut{Locale: l.Str("locale"), Name: l.Str("name"), CustomAppName: l.Str("customAppName")})
		}
		slices.SortFunc(gout.Localizations, func(a, b subsGroupLocOut) int { return strings.Compare(a.Locale, b.Locale) })
		if i >= subsMaxGroups {
			gout.Detail = "subscriptions not listed (group limit); call again with fewer groups or use asc_api_request"
			out.Groups = append(out.Groups, gout)
			continue
		}
		subs, err := subsOfGroup(ctx, env, g.ID)
		if err != nil {
			return nil, err
		}
		for _, s := range subs {
			out.ByState[s.State]++
		}
		out.Subscriptions += len(subs)
		gout.Subscriptions = subs
		out.Groups = append(out.Groups, gout)
	}
	if len(groups.Data) > subsMaxGroups {
		res.Warn(strconv.Itoa(len(groups.Data)-subsMaxGroups) + " group(s) listed without their subscriptions")
	}
	var all []subsOut
	for _, g := range out.Groups {
		all = append(all, g.Subscriptions...)
	}
	if ids := listIDs(all, func(s subsOut) string { return s.ID }); len(ids) > 0 {
		res.Then(tool.NextStep{Tool: "pricing_get", Args: map[string]any{"app": app.ID, "kind": prcKindSub, "products": ids}})
	}
	return res, nil
}

// subsOfGroup lists a group's subscriptions with versions and offer counts.
func subsOfGroup(ctx context.Context, env *tool.Env, groupID string) ([]subsOut, error) {
	q := url.Values{}
	for _, rel := range []string{"versions", "introductoryOffers", "promotionalOffers", "offerCodes", "winBackOffers"} {
		q.Set("limit["+rel+"]", "50")
	}
	res, err := env.ASC.List(ctx, "/v1/subscriptionGroups/"+url.PathEscape(groupID)+"/subscriptions", tool.ListOptions{
		Query:   q,
		Include: []string{"versions", "introductoryOffers", "promotionalOffers", "offerCodes", "winBackOffers"},
		Fields: map[string][]string{
			"subscriptions": {
				"name", "productId", "state", "subscriptionPeriod", "groupLevel", "familySharable",
				"versions", "introductoryOffers", "promotionalOffers", "offerCodes", "winBackOffers",
			},
			"subscriptionVersions":           {"version", "state"},
			"subscriptionIntroductoryOffers": {"offerMode"},
			"subscriptionPromotionalOffers":  {"offerMode"},
			"subscriptionOfferCodes":         {"offerMode"},
			"winBackOffers":                  {"offerMode"},
		},
		MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of group %s: %w", groupID, err)
	}
	inc := indexIncluded(res.Included)
	out := make([]subsOut, 0, len(res.Data))
	for _, s := range res.Data {
		lvl, _ := s.Int("groupLevel")
		fam, _ := s.Bool("familySharable")
		it := subsOut{
			ID: s.ID, ProductID: s.Str("productId"), Name: s.Str("name"), Period: s.Str("subscriptionPeriod"),
			GroupLevel: lvl, State: s.Str("state"), FamilySharable: fam,
			Offers: subsOffersOut{
				Introductory: len(s.RelIDs("introductoryOffers")), Promotional: len(s.RelIDs("promotionalOffers")),
				OfferCodes: len(s.RelIDs("offerCodes")), WinBack: len(s.RelIDs("winBackOffers")),
			},
		}
		if v, ok := latestVersion(related(s, "versions", "subscriptionVersions", inc)); ok {
			n, _ := v.Int("version")
			it.Version = &iapVersionOut{ID: v.ID, Version: n, State: v.Str("state")}
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b subsOut) int { return a.GroupLevel - b.GroupLevel })
	return out, nil
}

type subsGroupLocIn struct {
	Name          string `json:"name" jsonschema:"group display name"`
	CustomAppName string `json:"custom_app_name,omitempty" jsonschema:"app name shown in the subscription management UI"`
}

type subsUpsertIn struct {
	App                tool.AppRef               `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Group              string                    `json:"group" jsonschema:"subscription group reference name (max 64); created if missing"`
	GroupLocalizations map[string]subsGroupLocIn `json:"group_localizations,omitempty" jsonschema:"group display names by locale"`
	ProductID          string                    `json:"product_id,omitempty" jsonschema:"subscription product id; omit to upsert only the group"`
	Name               string                    `json:"name,omitempty" jsonschema:"subscription reference name (max 64); required to create"`
	Period             string                    `json:"period,omitempty"`
	GroupLevel         *int                      `json:"group_level,omitempty" jsonschema:"rank in the group, 1 = highest"`
	ReviewNote         *string                   `json:"review_note,omitempty"`
	FamilySharable     *bool                     `json:"family_sharable,omitempty" jsonschema:"cannot be turned off once on"`
	Localizations      map[string]locIn          `json:"localizations,omitempty" jsonschema:"by locale: display name (max 30) and description (max 45)"`
	ReviewScreenshot   string                    `json:"review_screenshot,omitempty" jsonschema:"path to a PNG/JPEG screenshot for App Review"`
	Availability       *availIn                  `json:"availability,omitempty" jsonschema:"territories it is sold in"`
	tool.ConfirmFlag
}

type subsUpsertOut struct {
	App                tool.App     `json:"app"`
	Group              upsertItem   `json:"group"`
	GroupVersion       *versionOut  `json:"group_version,omitempty"`
	GroupLocalizations []upsertItem `json:"group_localizations,omitempty"`
	Subscription       *upsertItem  `json:"subscription,omitempty"`
	ProductID          string       `json:"product_id,omitempty"`
	Version            *versionOut  `json:"version,omitempty"`
	Localizations      []upsertItem `json:"localizations,omitempty"`
	ReviewScreenshot   *upsertItem  `json:"review_screenshot,omitempty"`
	Availability       *upsertItem  `json:"availability,omitempty"`
	Failed             int          `json:"failed,omitempty"`
}

// subsFields are the sparse subscription fields upserts read back.
const subsFields = "name,productId,state,subscriptionPeriod,groupLevel,reviewNote,familySharable,appStoreReviewScreenshot"

func subsValidate(env *tool.Env, in subsUpsertIn) (glocs, locs map[string]map[string]any, err error) {
	if strings.TrimSpace(in.Group) == "" {
		return nil, nil, tool.InvalidArgs("group is required")
	}
	if err := checkLimit("group", applemeta.FieldIAPReferenceName, in.Group); err != nil {
		return nil, nil, err
	}
	glocs = make(map[string]map[string]any, len(in.GroupLocalizations))
	for k, l := range in.GroupLocalizations {
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, nil, tool.InvalidArgs("group_localizations: %v", err)
		}
		if _, dup := glocs[loc]; dup {
			return nil, nil, tool.InvalidArgs("group_localizations: locale %s given twice", loc)
		}
		if strings.TrimSpace(l.Name) == "" {
			return nil, nil, tool.InvalidArgs("group_localizations[%s].name is required", loc)
		}
		a := map[string]any{"name": l.Name}
		if l.CustomAppName != "" {
			a["customAppName"] = l.CustomAppName
		}
		glocs[loc] = a
	}
	if in.ProductID == "" {
		if in.Name != "" || in.Period != "" || in.GroupLevel != nil || in.ReviewNote != nil || in.FamilySharable != nil ||
			len(in.Localizations) > 0 || in.ReviewScreenshot != "" {
			return nil, nil, tool.InvalidArgs("subscription fields need product_id")
		}
		return glocs, nil, nil
	}
	if err := checkProductID(in.ProductID); err != nil {
		return nil, nil, err
	}
	if in.Name != "" {
		if err := checkLimit("name", applemeta.FieldIAPReferenceName, in.Name); err != nil {
			return nil, nil, err
		}
	}
	if in.GroupLevel != nil && *in.GroupLevel < 1 {
		return nil, nil, tool.InvalidArgs("group_level must be >= 1")
	}
	if locs, err = normLocs("localizations", in.Localizations); err != nil {
		return nil, nil, err
	}
	if err := checkScreenshot(env, in.ReviewScreenshot); err != nil {
		return nil, nil, err
	}
	return glocs, locs, nil
}

// subsPlan is what subscriptions_upsert read before its gate.
type subsPlan struct {
	group, sub, shot    tool.Resource
	groupFound, found   bool
	gver, ver           tool.Resource
	gverFound, verFound bool
	gexisting, existing []tool.Resource
	want, changed       map[string]any
	names               []string
	avail               *availPlan
}

func subsUpsert(ctx context.Context, env *tool.Env, in subsUpsertIn) (*tool.Result, error) {
	glocs, locs, err := subsValidate(env, in)
	if err != nil {
		return nil, err
	}
	if in.Availability != nil && in.ProductID == "" {
		return nil, tool.InvalidArgs("availability needs product_id")
	}
	if err := checkAvail(in.Availability); err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	var g upsertGate
	p, err := subsPlanAll(ctx, env, &g, app.ID, in, glocs, locs)
	if err != nil {
		return nil, err
	}
	wctx, gres := g.open(ctx, env, in.Confirm, "subscriptions_upsert "+in.Group+" "+in.ProductID+" in "+app.Name)
	if gres != nil {
		return gres, nil
	}

	out := subsUpsertOut{App: app, ProductID: in.ProductID}
	group := p.group
	if p.groupFound {
		out.Group = upsertItem{Kind: "subscriptionGroups", ID: group.ID, Action: actionUnchanged}
	} else {
		group, err = postResource(wctx, env, "/v1/subscriptionGroups", "subscriptionGroups",
			map[string]any{"referenceName": in.Group}, map[string][2]string{"app": {"apps", app.ID}})
		switch {
		case err == nil:
			out.Group = upsertItem{Kind: "subscriptionGroups", ID: group.ID, Action: actionCreated}
		case tool.IsConflict(err):
			if group, _, err = subsFindGroup(ctx, env, app.ID, in.Group); err != nil {
				return nil, err
			}
			out.Group = upsertItem{
				Kind: "subscriptionGroups", ID: group.ID, Action: actionAlready,
				Note: "created elsewhere; rerun to preview changes to it",
			}
			return tool.Already(out), nil
		default:
			return nil, fmt.Errorf("create subscription group %q: %w", in.Group, err)
		}
	}
	items := []upsertItem{out.Group}

	if len(glocs) > 0 {
		v, litems, err := writeLocs(wctx, env, groupVersions, group.ID, p.gverFound, p.gver, p.gexisting, glocs)
		if err != nil {
			return nil, err
		}
		out.GroupVersion, out.GroupLocalizations = &v, litems
		items = append(items, litems...)
		if v.Created {
			items = append(items, upsertItem{Kind: "subscriptionGroupVersions", Action: actionCreated})
		}
	}

	if in.ProductID != "" {
		sub, it, err := subsWriteProduct(wctx, env, app.ID, group.ID, in, p)
		if err != nil {
			return nil, err
		}
		out.Subscription = &it
		items = append(items, it)
		if it.Action == actionAlready {
			// Created by someone else meanwhile: nothing else was previewed for it.
			return tool.Already(out), nil
		}
		if len(locs) > 0 {
			v, litems, err := writeLocs(wctx, env, subVersions, sub.ID, p.verFound, p.ver, p.existing, locs)
			if err != nil {
				return nil, err
			}
			out.Version, out.Localizations = &v, litems
			items = append(items, litems...)
			if v.Created {
				items = append(items, upsertItem{Kind: "subscriptionVersions", Action: actionCreated})
			}
		}
		if in.ReviewScreenshot != "" {
			it := uploadScreenshot(wctx, env, subShot, sub.ID, in.ReviewScreenshot, p.shot)
			out.ReviewScreenshot = &it
			items = append(items, it)
		}
		if p.avail != nil {
			it := applyAvail(wctx, env, subAvail, sub.ID, p.avail)
			out.Availability = &it
			items = append(items, it)
		}
	}

	out.Failed = failures(items)
	if allUnchanged(items) {
		return tool.Already(out), nil
	}
	res := tool.OK(out).Then(
		tool.NextStep{Tool: "subscriptions_list", Args: map[string]any{"app": app.ID}},
		tool.NextStep{Tool: "pricing_plan", Args: map[string]any{"app": app.ID}},
	)
	if out.Failed > 0 {
		res.Warn(strconv.Itoa(out.Failed) + " item(s) failed; see the error fields and rerun (the upsert is idempotent)")
	}
	return res, nil
}

// subsPlanAll reads the group, subscription, open versions and availability
// and records every write the upsert would make in g. It never writes.
func subsPlanAll(ctx context.Context, env *tool.Env, g *upsertGate, appID string, in subsUpsertIn, glocs, locs map[string]map[string]any) (*subsPlan, error) {
	var err error
	p := &subsPlan{want: map[string]any{}}
	if p.group, p.groupFound, err = subsFindGroup(ctx, env, appID, in.Group); err != nil {
		return nil, err
	}
	if !p.groupFound {
		g.add(gateAdditive, "POST /v1/subscriptionGroups "+in.Group)
	}
	if len(glocs) > 0 {
		if p.groupFound {
			if p.gver, p.gexisting, p.gverFound, err = findOpenVersion(ctx, env, groupVersions, p.group.ID); err != nil {
				return nil, err
			}
		}
		if err := g.planLocs(groupVersions, p.gexisting, glocs); err != nil {
			return nil, err
		}
	}
	if in.ProductID == "" {
		return p, nil
	}
	if in.Name != "" {
		p.want["name"] = in.Name
	}
	if in.Period != "" {
		p.want["subscriptionPeriod"] = in.Period
	}
	if in.GroupLevel != nil {
		p.want["groupLevel"] = *in.GroupLevel
	}
	if in.ReviewNote != nil {
		p.want["reviewNote"] = *in.ReviewNote
	}
	if in.FamilySharable != nil {
		p.want["familySharable"] = *in.FamilySharable
	}
	if p.groupFound {
		if p.sub, p.shot, p.found, err = subsFind(ctx, env, p.group.ID, in.ProductID); err != nil {
			return nil, err
		}
	}
	if !p.found {
		if in.Name == "" {
			return nil, tool.InvalidArgs("subscription %q does not exist; name is required to create it", in.ProductID)
		}
		g.add(gateHuman, "POST /v1/subscriptions "+in.ProductID+" in group "+in.Group)
		g.irreversible("product id " + in.ProductID + " is used up for good: it can never be reused, even after deletion")
		if on, _ := p.want["familySharable"].(bool); on {
			g.irreversible("family sharing cannot be turned off again; family members get it from one purchase")
		}
	} else {
		if p.changed, p.names, err = attrDiff(p.sub, p.want); err != nil {
			return nil, err
		}
		g.planAttrs("subscription "+in.ProductID, "/v1/subscriptions/"+p.sub.ID, p.sub, p.changed, "groupLevel")
		if _, ok := p.changed["groupLevel"]; ok {
			g.p.Irreversible = append(g.p.Irreversible, "changing the group level changes upgrade/downgrade billing for existing subscribers")
		}
	}
	if len(locs) > 0 {
		if p.found {
			if p.ver, p.existing, p.verFound, err = findOpenVersion(ctx, env, subVersions, p.sub.ID); err != nil {
				return nil, err
			}
		}
		if err := g.planLocs(subVersions, p.existing, locs); err != nil {
			return nil, err
		}
	}
	if in.ReviewScreenshot != "" && p.shot.ID == "" {
		g.add(gateAdditive, "POST "+subShot.CreatePath+" "+in.ReviewScreenshot)
	}
	if p.avail, err = planAvail(ctx, env, subAvail, p.sub.ID, in.Availability); err != nil {
		return nil, err
	}
	p.avail.gate(g, subAvail)
	return p, nil
}

// subsWriteProduct creates the planned subscription or PATCHes its planned
// diff. A 409 on create triggers a read-back across all of the app's groups.
func subsWriteProduct(ctx context.Context, env *tool.Env, appID, groupID string, in subsUpsertIn, p *subsPlan) (tool.Resource, upsertItem, error) {
	it := upsertItem{Kind: "subscriptions"}
	if p.found {
		it.ID, it.Action = p.sub.ID, actionUnchanged
		if len(p.changed) > 0 {
			if err := patch(ctx, env, "/v1/subscriptions/"+url.PathEscape(p.sub.ID), "subscriptions", p.sub.ID, p.changed); err != nil {
				return p.sub, it, fmt.Errorf("update subscription %s: %w", in.ProductID, err)
			}
			it.Action, it.Changed = actionUpdated, p.names
		}
		return p.sub, it, nil
	}
	attrs := map[string]any{"productId": in.ProductID}
	for k, v := range p.want {
		attrs[k] = v
	}
	created, cerr := postResource(ctx, env, "/v1/subscriptions", "subscriptions", attrs, map[string][2]string{"group": {"subscriptionGroups", groupID}})
	switch {
	case cerr == nil:
		it.ID, it.Action = created.ID, actionCreated
		return created, it, nil
	case tool.IsConflict(cerr):
		gid, rerr := subsFindAnywhere(ctx, env, appID, in.ProductID)
		if rerr != nil {
			return created, it, rerr
		}
		switch gid {
		case "":
			return created, it, fmt.Errorf("create subscription %s: Apple reports a conflict but the product is not in this app; product ids are unique per account and cannot be reused: %w", in.ProductID, tool.ErrInvalidArgs)
		case groupID:
		default:
			return created, it, tool.InvalidArgs("subscription %s already exists in another group (%s); subscriptions cannot move between groups", in.ProductID, gid)
		}
		sub, _, found, err := subsFind(ctx, env, groupID, in.ProductID)
		if err != nil {
			return sub, it, err
		}
		if !found {
			return sub, it, fmt.Errorf("subscription %s: conflict but not readable: %w", in.ProductID, tool.ErrOutcomeUnknown)
		}
		it.ID, it.Action, it.Note = sub.ID, actionAlready, "created elsewhere; rerun to update it"
		return sub, it, nil
	default:
		return created, it, fmt.Errorf("create subscription %s: %w", in.ProductID, cerr)
	}
}

// subsFindGroup looks a subscription group up by exact reference name.
func subsFindGroup(ctx context.Context, env *tool.Env, appID, ref string) (tool.Resource, bool, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(appID)+"/subscriptionGroups", url.Values{
		"filter[referenceName]":      {ref},
		"fields[subscriptionGroups]": {"referenceName"},
		"limit":                      {"200"},
	})
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("look up subscription group %q: %w", ref, err)
	}
	for _, g := range resp.Data {
		if g.Str("referenceName") == ref {
			return g, true, nil
		}
	}
	return tool.Resource{}, false, nil
}

// subsFind looks a subscription up in a group by exact product id, with its
// review screenshot.
func subsFind(ctx context.Context, env *tool.Env, groupID, productID string) (sub, shot tool.Resource, found bool, err error) {
	resp, err := env.ASC.Get(ctx, "/v1/subscriptionGroups/"+url.PathEscape(groupID)+"/subscriptions", url.Values{
		"filter[productId]":     {productID},
		"include":               {"appStoreReviewScreenshot"},
		"fields[subscriptions]": {subsFields},
		"fields[subscriptionAppStoreReviewScreenshots]": {shotFields},
		"limit": {"200"},
	})
	if err != nil {
		return sub, shot, false, fmt.Errorf("look up subscription %s: %w", productID, err)
	}
	inc := indexIncluded(resp.Included)
	for _, r := range resp.Data {
		if r.Str("productId") == productID {
			if id := r.RelID("appStoreReviewScreenshot"); id != "" {
				shot = inc["subscriptionAppStoreReviewScreenshots/"+id]
			}
			return r, shot, true, nil
		}
	}
	return sub, shot, false, nil
}

// subsFindAnywhere returns the id of the app's group holding productID, or "".
func subsFindAnywhere(ctx context.Context, env *tool.Env, appID, productID string) (string, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/subscriptionGroups", tool.ListOptions{
		Query:   url.Values{"limit[subscriptions]": {"50"}},
		Include: []string{"subscriptions"},
		Fields: map[string][]string{
			"subscriptionGroups": {"referenceName", "subscriptions"},
			"subscriptions":      {"productId"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("read back subscription %s: %w", productID, err)
	}
	inc := indexIncluded(res.Included)
	for _, g := range res.Data {
		for _, s := range related(g, "subscriptions", "subscriptions", inc) {
			if s.Str("productId") == productID {
				return g.ID, nil
			}
		}
	}
	return "", nil
}
