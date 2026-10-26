package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

var (
	iapTypes  = []any{"CONSUMABLE", "NON_CONSUMABLE", "NON_RENEWING_SUBSCRIPTION"}
	iapStates = []any{
		"MISSING_METADATA", "WAITING_FOR_UPLOAD", "PROCESSING_CONTENT", "READY_TO_SUBMIT", "WAITING_FOR_REVIEW",
		"IN_REVIEW", "DEVELOPER_ACTION_NEEDED", "PENDING_BINARY_APPROVAL", "APPROVED",
		"DEVELOPER_REMOVED_FROM_SALE", "REMOVED_FROM_SALE", "REJECTED",
	}
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "iap_list", Toolset: tool.Monetization, Class: tool.Read, Idempotent: true,
		Title: "List in-app purchases",
		Description: "In-app purchases (v2) of an app in one request: product id, type, state, latest version state " +
			"and localizations. Filter by state or type. Prices: use pricing_get. Subscriptions: subscriptions_list.",
		Enums: map[string][]any{"state": iapStates, "type": iapTypes},
	}, iapList))
	tool.Register(tool.New(tool.Meta{
		Name: "iap_upsert", Toolset: tool.Monetization, Class: tool.Write, Idempotent: true, Confirm: tool.ConfirmByFlag,
		Title: "Create or update an in-app purchase",
		Description: "Create an IAP by product_id or PATCH only changed fields, plus localizations, review screenshot " +
			"and availability. Overwrites need confirm:true; creating (burns the id) and family sharing on need a human.",
		Enums: map[string][]any{"type": iapTypes},
	}, iapUpsert))
}

type iapListIn struct {
	App    tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	State  string      `json:"state,omitempty"`
	Type   string      `json:"type,omitempty"`
	Limit  int         `json:"limit,omitempty" jsonschema:"default 200, max 5000"`
	Cursor string      `json:"cursor,omitempty" jsonschema:"next_cursor"`
}

type iapLocOut struct {
	Locale      string         `json:"locale"`
	Name        tool.Untrusted `json:"name,omitempty"`
	Description tool.Untrusted `json:"description,omitempty"`
}

type iapVersionOut struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
	State   string `json:"state,omitempty"`
}

type iapOut struct {
	ID             string         `json:"id"`
	ProductID      string         `json:"product_id"`
	ReferenceName  tool.Untrusted `json:"reference_name"`
	Type           string         `json:"type"`
	State          string         `json:"state"`
	FamilySharable bool           `json:"family_sharable,omitempty"`
	Version        *iapVersionOut `json:"version,omitempty"`
	Localizations  []iapLocOut    `json:"localizations,omitempty"`
}

type iapListOut struct {
	App        tool.App       `json:"app"`
	Total      int            `json:"total,omitempty"`
	Returned   int            `json:"returned"`
	ByState    map[string]int `json:"by_state"`
	Items      []iapOut       `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func iapList(ctx context.Context, env *tool.Env, in iapListIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"limit[inAppPurchaseLocalizations]": {"50"},
		"limit[versions]":                   {"10"},
	}
	if in.State != "" {
		q.Set("filter[state]", in.State)
	}
	if in.Type != "" {
		q.Set("filter[inAppPurchaseType]", in.Type)
	}
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/inAppPurchasesV2", tool.ListOptions{
		Query:   q,
		Include: []string{"inAppPurchaseLocalizations", "versions"},
		Fields: map[string][]string{
			"inAppPurchases":             {"name", "productId", "inAppPurchaseType", "state", "familySharable", "inAppPurchaseLocalizations", "versions"},
			"inAppPurchaseLocalizations": {"locale", "name", "description"},
			"inAppPurchaseVersions":      {"version", "state"},
		},
		MaxItems: in.Limit,
		Cursor:   in.Cursor,
	})
	if err != nil {
		return nil, fmt.Errorf("list in-app purchases: %w", err)
	}
	inc := indexIncluded(res.Included)
	out := iapListOut{App: app, Total: res.Total, Returned: res.Returned, ByState: map[string]int{}, Items: []iapOut{}, NextCursor: res.NextCursor}
	for _, r := range res.Data {
		fam, _ := r.Bool("familySharable")
		it := iapOut{
			ID: r.ID, ProductID: r.Str("productId"), ReferenceName: r.Text("name"),
			Type: r.Str("inAppPurchaseType"), State: r.Str("state"), FamilySharable: fam,
		}
		if v, ok := latestVersion(related(r, "versions", "inAppPurchaseVersions", inc)); ok {
			n, _ := v.Int("version")
			it.Version = &iapVersionOut{ID: v.ID, Version: n, State: v.Str("state")}
		}
		it.Localizations = iapLocs(related(r, "inAppPurchaseLocalizations", "inAppPurchaseLocalizations", inc))
		out.ByState[it.State]++
		out.Items = append(out.Items, it)
	}
	res2 := tool.OK(out)
	if ids := listIDs(out.Items, func(it iapOut) string { return it.ID }); len(ids) > 0 {
		res2.Then(tool.NextStep{Tool: "pricing_get", Args: map[string]any{"app": app.ID, "kind": "iap", "products": ids}})
	}
	return res2, nil
}

// iapLocs renders localizations sorted by locale.
func iapLocs(rs []tool.Resource) []iapLocOut {
	out := make([]iapLocOut, 0, len(rs))
	for _, l := range rs {
		out = append(out, iapLocOut{Locale: l.Str("locale"), Name: l.Text("name"), Description: l.Text("description")})
	}
	slices.SortFunc(out, func(a, b iapLocOut) int { return strings.Compare(a.Locale, b.Locale) })
	return out
}

type iapUpsertIn struct {
	App              tool.AppRef      `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	ProductID        string           `json:"product_id" jsonschema:"product identifier; the lookup key"`
	Type             string           `json:"type,omitempty" jsonschema:"required to create; cannot change later"`
	ReferenceName    string           `json:"reference_name,omitempty" jsonschema:"internal name (max 64); required to create"`
	ReviewNote       *string          `json:"review_note,omitempty"`
	FamilySharable   *bool            `json:"family_sharable,omitempty" jsonschema:"cannot be turned off once on"`
	Localizations    map[string]locIn `json:"localizations,omitempty" jsonschema:"by locale: display name (max 30) and description (max 45)"`
	ReviewScreenshot string           `json:"review_screenshot,omitempty" jsonschema:"path to a PNG/JPEG screenshot for App Review"`
	Availability     *availIn         `json:"availability,omitempty" jsonschema:"territories it is sold in"`
	tool.ConfirmFlag
}

type iapUpsertOut struct {
	App              tool.App     `json:"app"`
	Product          upsertItem   `json:"product"`
	ProductID        string       `json:"product_id"`
	Version          *versionOut  `json:"version,omitempty"`
	Localizations    []upsertItem `json:"localizations,omitempty"`
	ReviewScreenshot *upsertItem  `json:"review_screenshot,omitempty"`
	Availability     *upsertItem  `json:"availability,omitempty"`
	Failed           int          `json:"failed,omitempty"`
}

// iapProductFields are the sparse product fields upserts read back.
var iapProductFields = "name,productId,inAppPurchaseType,state,reviewNote,familySharable,appStoreReviewScreenshot"

func iapUpsert(ctx context.Context, env *tool.Env, in iapUpsertIn) (*tool.Result, error) {
	if err := checkProductID(in.ProductID); err != nil {
		return nil, err
	}
	if in.ReferenceName != "" {
		if err := checkLimit("reference_name", applemeta.FieldIAPReferenceName, in.ReferenceName); err != nil {
			return nil, err
		}
	}
	locs, err := normLocs("localizations", in.Localizations)
	if err != nil {
		return nil, err
	}
	if err := checkScreenshot(env, in.ReviewScreenshot); err != nil {
		return nil, err
	}
	if err := checkAvail(in.Availability); err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}

	want := map[string]any{}
	if in.ReferenceName != "" {
		want["name"] = in.ReferenceName
	}
	if in.ReviewNote != nil {
		want["reviewNote"] = *in.ReviewNote
	}
	if in.FamilySharable != nil {
		want["familySharable"] = *in.FamilySharable
	}

	// Plan: read everything first, write nothing until the gate passes.
	var g upsertGate
	cur, shot, found, err := iapFind(ctx, env, app.ID, in.ProductID)
	if err != nil {
		return nil, err
	}
	var (
		changed  map[string]any
		names    []string
		ver      tool.Resource
		verFound bool
		existing []tool.Resource
	)
	if !found {
		if in.Type == "" || in.ReferenceName == "" {
			return nil, tool.InvalidArgs("product %q does not exist; type and reference_name are required to create it", in.ProductID)
		}
		g.add(gateHuman, "POST /v2/inAppPurchases "+in.ProductID+" ("+in.Type+")")
		g.irreversible("product id " + in.ProductID + " is used up for good: it can never be reused, even after deletion")
		if on, _ := want["familySharable"].(bool); on {
			g.irreversible("family sharing cannot be turned off again; family members get it from one purchase")
		}
		if len(locs) > 0 {
			g.add(gateAdditive, "POST /v2/inAppPurchaseLocalizations "+strings.Join(sortedKeys(locs), ","))
		}
	} else {
		if in.Type != "" && cur.Str("inAppPurchaseType") != "" && in.Type != cur.Str("inAppPurchaseType") {
			return nil, tool.InvalidArgs("product %s is %s; the type of an in-app purchase cannot change", in.ProductID, cur.Str("inAppPurchaseType"))
		}
		if changed, names, err = attrDiff(cur, want); err != nil {
			return nil, err
		}
		g.planAttrs("inAppPurchase "+in.ProductID, "/v2/inAppPurchases/"+cur.ID, cur, changed)
		if len(locs) > 0 {
			if ver, existing, verFound, err = findOpenVersion(ctx, env, iapVersions, cur.ID); err != nil {
				return nil, err
			}
			if err := g.planLocs(iapVersions, existing, locs); err != nil {
				return nil, err
			}
		}
	}
	if in.ReviewScreenshot != "" && shot.ID == "" {
		g.add(gateAdditive, "POST "+iapShot.CreatePath+" "+in.ReviewScreenshot)
	}
	avail, err := planAvail(ctx, env, iapAvail, cur.ID, in.Availability)
	if err != nil {
		return nil, err
	}
	avail.gate(&g, iapAvail)

	wctx, gres := g.open(ctx, env, in.Confirm, "iap_upsert "+in.ProductID+" in "+app.Name)
	if gres != nil {
		return gres, nil
	}

	out := iapUpsertOut{App: app, ProductID: in.ProductID}
	if !found {
		attrs := map[string]any{"productId": in.ProductID, "inAppPurchaseType": in.Type}
		for k, v := range want {
			attrs[k] = v
		}
		created, err := postResource(wctx, env, "/v2/inAppPurchases", "inAppPurchases", attrs, map[string][2]string{"app": {"apps", app.ID}})
		switch {
		case err == nil:
			cur, out.Product = created, upsertItem{Kind: "inAppPurchases", ID: created.ID, Action: actionCreated}
		case tool.IsConflict(err):
			// Read back: the product exists (maybe created by a racing call).
			// Its fields are left alone: they were not previewed.
			cur, shot, found, err = iapFind(ctx, env, app.ID, in.ProductID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("create in-app purchase %s: Apple reports a conflict but the product is not in this app; product ids are unique per account and cannot be reused: %w", in.ProductID, tool.ErrInvalidArgs)
			}
			out.Product = upsertItem{Kind: "inAppPurchases", ID: cur.ID, Action: actionAlready, Note: "created elsewhere; rerun to update it"}
			return tool.Already(out), nil
		default:
			return nil, fmt.Errorf("create in-app purchase %s: %w", in.ProductID, err)
		}
	} else {
		out.Product = upsertItem{Kind: "inAppPurchases", ID: cur.ID, Action: actionUnchanged}
		if len(changed) > 0 {
			if err := patch(wctx, env, "/v2/inAppPurchases/"+url.PathEscape(cur.ID), "inAppPurchases", cur.ID, changed); err != nil {
				return nil, fmt.Errorf("update in-app purchase %s: %w", in.ProductID, err)
			}
			out.Product.Action, out.Product.Changed = actionUpdated, names
		}
	}

	items := []upsertItem{out.Product}
	if len(locs) > 0 {
		v, litems, err := writeLocs(wctx, env, iapVersions, cur.ID, verFound, ver, existing, locs)
		if err != nil {
			return nil, err
		}
		out.Version, out.Localizations = &v, litems
		items = append(items, litems...)
		if v.Created {
			items = append(items, upsertItem{Kind: "inAppPurchaseVersions", Action: actionCreated})
		}
	}
	if in.ReviewScreenshot != "" {
		it := uploadScreenshot(wctx, env, iapShot, cur.ID, in.ReviewScreenshot, shot)
		out.ReviewScreenshot = &it
		items = append(items, it)
	}
	if avail != nil {
		it := applyAvail(wctx, env, iapAvail, cur.ID, avail)
		out.Availability = &it
		items = append(items, it)
	}
	out.Failed = failures(items)
	if allUnchanged(items) {
		return tool.Already(out), nil
	}
	res := upsertResult(out, items,
		tool.NextStep{Tool: "iap_list", Args: map[string]any{"app": app.ID}},
		tool.NextStep{Tool: "pricing_plan", Args: map[string]any{"app": app.ID, "kind": "iap", "product_id": in.ProductID}},
	)
	if out.Failed > 0 && !res.IsError {
		res.Warn("rerun to retry the failed items (the upsert is idempotent)")
	}
	return res, nil
}

// listIDs returns up to 10 ids of items (pricing_get's products cap).
func listIDs[T any](items []T, id func(T) string) []string {
	out := make([]string, 0, min(len(items), 10))
	for _, it := range items[:min(len(items), 10)] {
		out = append(out, id(it))
	}
	return out
}

// iapFind looks an IAP up by exact product id, with its review screenshot.
func iapFind(ctx context.Context, env *tool.Env, appID, productID string) (product, shot tool.Resource, found bool, err error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(appID)+"/inAppPurchasesV2", url.Values{
		"filter[productId]":      {productID},
		"include":                {"appStoreReviewScreenshot"},
		"fields[inAppPurchases]": {iapProductFields},
		"fields[inAppPurchaseAppStoreReviewScreenshots]": {shotFields},
		"limit": {"200"},
	})
	if err != nil {
		return product, shot, false, fmt.Errorf("look up in-app purchase %s: %w", productID, err)
	}
	inc := indexIncluded(resp.Included)
	for _, r := range resp.Data {
		if r.Str("productId") == productID {
			if id := r.RelID("appStoreReviewScreenshot"); id != "" {
				shot = inc["inAppPurchaseAppStoreReviewScreenshots/"+id]
			}
			return r, shot, true, nil
		}
	}
	return product, shot, false, nil
}
