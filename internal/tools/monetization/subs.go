package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
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
		Name: "subscriptions_upsert", Toolset: tool.Monetization, Class: tool.Write, Idempotent: true,
		Title: "Create or update a subscription",
		Description: "Find or create a subscription group by reference name, then a subscription by product_id (PATCH only " +
			"changed fields; never re-creates a product id). Writes localizations on the open versions and a review " +
			"screenshot. Never deletes.",
		Enums: map[string][]any{"period": subsPeriods},
	}, subsUpsert))
}

type subsListIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
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
	ginc := s12aIncluded(groups.Included)
	out := subsListOut{App: app, Groups: []subsGroupOut{}, ByState: map[string]int{}}
	res := tool.OK(&out)
	for i, g := range groups.Data {
		gout := subsGroupOut{ID: g.ID, ReferenceName: g.Str("referenceName"), Subscriptions: []subsOut{}}
		for _, l := range s12aRelated(g, "subscriptionGroupLocalizations", "subscriptionGroupLocalizations", ginc) {
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
	inc := s12aIncluded(res.Included)
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
		if v, ok := s12aLatestVersion(s12aRelated(s, "versions", "subscriptionVersions", inc)); ok {
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
	App                tool.AppRef               `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Group              string                    `json:"group" jsonschema:"subscription group reference name (max 64); created if missing"`
	GroupLocalizations map[string]subsGroupLocIn `json:"group_localizations,omitempty" jsonschema:"group display names by locale"`
	ProductID          string                    `json:"product_id,omitempty" jsonschema:"subscription product id; omit to upsert only the group"`
	Name               string                    `json:"name,omitempty" jsonschema:"subscription reference name (max 64); required to create"`
	Period             string                    `json:"period,omitempty"`
	GroupLevel         *int                      `json:"group_level,omitempty" jsonschema:"rank in the group, 1 = highest"`
	ReviewNote         *string                   `json:"review_note,omitempty"`
	FamilySharable     *bool                     `json:"family_sharable,omitempty" jsonschema:"cannot be turned off once on"`
	Localizations      map[string]s12aLocIn      `json:"localizations,omitempty" jsonschema:"by locale: display name (max 30) and description (max 45)"`
	ReviewScreenshot   string                    `json:"review_screenshot,omitempty" jsonschema:"path to a PNG/JPEG screenshot for App Review"`
}

type subsUpsertOut struct {
	App                tool.App        `json:"app"`
	Group              s12aItem        `json:"group"`
	GroupVersion       *s12aVersionOut `json:"group_version,omitempty"`
	GroupLocalizations []s12aItem      `json:"group_localizations,omitempty"`
	Subscription       *s12aItem       `json:"subscription,omitempty"`
	ProductID          string          `json:"product_id,omitempty"`
	Version            *s12aVersionOut `json:"version,omitempty"`
	Localizations      []s12aItem      `json:"localizations,omitempty"`
	ReviewScreenshot   *s12aItem       `json:"review_screenshot,omitempty"`
	Failed             int             `json:"failed,omitempty"`
}

// subsFields are the sparse subscription fields upserts read back.
const subsFields = "name,productId,state,subscriptionPeriod,groupLevel,reviewNote,familySharable,appStoreReviewScreenshot"

func subsValidate(env *tool.Env, in subsUpsertIn) (glocs, locs map[string]map[string]any, err error) {
	if strings.TrimSpace(in.Group) == "" {
		return nil, nil, s12aInvalid("group is required")
	}
	if err := s12aCheckLimit("group", applemeta.FieldIAPReferenceName, in.Group); err != nil {
		return nil, nil, err
	}
	glocs = make(map[string]map[string]any, len(in.GroupLocalizations))
	for k, l := range in.GroupLocalizations {
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, nil, s12aInvalid("group_localizations: %v", err)
		}
		if _, dup := glocs[loc]; dup {
			return nil, nil, s12aInvalid("group_localizations: locale %s given twice", loc)
		}
		if strings.TrimSpace(l.Name) == "" {
			return nil, nil, s12aInvalid("group_localizations[%s].name is required", loc)
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
			return nil, nil, s12aInvalid("subscription fields need product_id")
		}
		return glocs, nil, nil
	}
	if err := s12aCheckProductID(in.ProductID); err != nil {
		return nil, nil, err
	}
	if in.Name != "" {
		if err := s12aCheckLimit("name", applemeta.FieldIAPReferenceName, in.Name); err != nil {
			return nil, nil, err
		}
	}
	if in.GroupLevel != nil && *in.GroupLevel < 1 {
		return nil, nil, s12aInvalid("group_level must be >= 1")
	}
	if locs, err = s12aNormLocs("localizations", in.Localizations); err != nil {
		return nil, nil, err
	}
	if err := s12aCheckScreenshot(env, in.ReviewScreenshot); err != nil {
		return nil, nil, err
	}
	return glocs, locs, nil
}

func subsUpsert(ctx context.Context, env *tool.Env, in subsUpsertIn) (*tool.Result, error) {
	glocs, locs, err := subsValidate(env, in)
	if err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	out := subsUpsertOut{App: app, ProductID: in.ProductID}

	group, found, err := subsFindGroup(ctx, env, app.ID, in.Group)
	if err != nil {
		return nil, err
	}
	if found {
		out.Group = s12aItem{Kind: "subscriptionGroups", ID: group.ID, Action: s12aUnchanged}
	} else {
		group, err = s12aPost(ctx, env, "/v1/subscriptionGroups", "subscriptionGroups",
			map[string]any{"referenceName": in.Group}, map[string][2]string{"app": {"apps", app.ID}})
		switch {
		case err == nil:
			out.Group = s12aItem{Kind: "subscriptionGroups", ID: group.ID, Action: s12aCreated}
		case s12aConflict(err):
			if group, found, err = subsFindGroup(ctx, env, app.ID, in.Group); err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("create subscription group %q: Apple reports a conflict but no such group is readable: %w", in.Group, tool.ErrOutcomeUnknown)
			}
			out.Group = s12aItem{Kind: "subscriptionGroups", ID: group.ID, Action: s12aAlready}
		default:
			return nil, fmt.Errorf("create subscription group %q: %w", in.Group, err)
		}
	}
	items := []s12aItem{out.Group}

	if len(glocs) > 0 {
		v, existing, err := s12aOpenVersion(ctx, env, s12aGroupVersions, group.ID)
		if err != nil {
			return nil, err
		}
		out.GroupVersion = &v
		out.GroupLocalizations = s12aUpsertLocs(ctx, env, s12aGroupVersions, v.ID, existing, glocs)
		items = append(items, out.GroupLocalizations...)
		if v.Created {
			items = append(items, s12aItem{Kind: "subscriptionGroupVersions", Action: s12aCreated})
		}
	}

	if in.ProductID != "" {
		sub, shot, it, err := subsUpsertProduct(ctx, env, app.ID, out.Group, in)
		if err != nil {
			return nil, err
		}
		out.Subscription = &it
		items = append(items, it)
		if len(locs) > 0 {
			v, existing, err := s12aOpenVersion(ctx, env, s12aSubVersions, sub.ID)
			if err != nil {
				return nil, err
			}
			out.Version = &v
			out.Localizations = s12aUpsertLocs(ctx, env, s12aSubVersions, v.ID, existing, locs)
			items = append(items, out.Localizations...)
			if v.Created {
				items = append(items, s12aItem{Kind: "subscriptionVersions", Action: s12aCreated})
			}
		}
		if in.ReviewScreenshot != "" {
			it := s12aScreenshot(ctx, env, s12aSubShot, sub.ID, in.ReviewScreenshot, shot)
			out.ReviewScreenshot = &it
			items = append(items, it)
		}
	}

	out.Failed = s12aFailures(items)
	if s12aAllUnchanged(items) {
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

// subsUpsertProduct finds the subscription by product id in the group (skipped
// for a just-created group) and PATCHes its changed fields, or creates it.
// A 409 on create triggers a read-back across all of the app's groups.
func subsUpsertProduct(ctx context.Context, env *tool.Env, appID string, group s12aItem, in subsUpsertIn) (sub, shot tool.Resource, it s12aItem, err error) {
	want := map[string]any{}
	if in.Name != "" {
		want["name"] = in.Name
	}
	if in.Period != "" {
		want["subscriptionPeriod"] = in.Period
	}
	if in.GroupLevel != nil {
		want["groupLevel"] = *in.GroupLevel
	}
	if in.ReviewNote != nil {
		want["reviewNote"] = *in.ReviewNote
	}
	if in.FamilySharable != nil {
		want["familySharable"] = *in.FamilySharable
	}
	it = s12aItem{Kind: "subscriptions"}
	found := false
	if group.Action != s12aCreated {
		if sub, shot, found, err = subsFind(ctx, env, group.ID, in.ProductID); err != nil {
			return sub, shot, it, err
		}
	}
	if !found {
		if in.Name == "" {
			return sub, shot, it, s12aInvalid("subscription %q does not exist; name is required to create it", in.ProductID)
		}
		attrs := map[string]any{"productId": in.ProductID}
		for k, v := range want {
			attrs[k] = v
		}
		created, cerr := s12aPost(ctx, env, "/v1/subscriptions", "subscriptions", attrs, map[string][2]string{"group": {"subscriptionGroups", group.ID}})
		switch {
		case cerr == nil:
			it.ID, it.Action = created.ID, s12aCreated
			return created, shot, it, nil
		case s12aConflict(cerr):
			gid, rerr := subsFindAnywhere(ctx, env, appID, in.ProductID)
			if rerr != nil {
				return sub, shot, it, rerr
			}
			switch gid {
			case "":
				return sub, shot, it, fmt.Errorf("create subscription %s: Apple reports a conflict but the product is not in this app; product ids are unique per account and cannot be reused: %w", in.ProductID, tool.ErrInvalidArgs)
			case group.ID:
			default:
				return sub, shot, it, s12aInvalid("subscription %s already exists in another group (%s); subscriptions cannot move between groups", in.ProductID, gid)
			}
			if sub, shot, found, err = subsFind(ctx, env, group.ID, in.ProductID); err != nil {
				return sub, shot, it, err
			}
			if !found {
				return sub, shot, it, fmt.Errorf("subscription %s: conflict but not readable: %w", in.ProductID, tool.ErrOutcomeUnknown)
			}
			it.Action = s12aAlready
		default:
			return sub, shot, it, fmt.Errorf("create subscription %s: %w", in.ProductID, cerr)
		}
	}
	it.ID = sub.ID
	if it.Action == "" {
		it.Action = s12aUnchanged
	}
	changed, names, err := s12aDiff(sub, want)
	if err != nil {
		return sub, shot, it, err
	}
	if len(changed) > 0 {
		if err := s12aPatch(ctx, env, "/v1/subscriptions/"+url.PathEscape(sub.ID), "subscriptions", sub.ID, changed); err != nil {
			return sub, shot, it, fmt.Errorf("update subscription %s: %w", in.ProductID, err)
		}
		it.Action, it.Changed = s12aUpdated, names
	}
	return sub, shot, it, nil
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
		"fields[subscriptionAppStoreReviewScreenshots]": {s12aShotFields},
		"limit": {"200"},
	})
	if err != nil {
		return sub, shot, false, fmt.Errorf("look up subscription %s: %w", productID, err)
	}
	inc := s12aIncluded(resp.Included)
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
	inc := s12aIncluded(res.Included)
	for _, g := range res.Data {
		for _, s := range s12aRelated(g, "subscriptions", "subscriptions", inc) {
			if s.Str("productId") == productID {
				return g.ID, nil
			}
		}
	}
	return "", nil
}
