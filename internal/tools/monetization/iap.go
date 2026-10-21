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
		Name: "iap_upsert", Toolset: tool.Monetization, Class: tool.Write, Idempotent: true,
		Title: "Create or update an in-app purchase",
		Description: "Create an IAP by product_id or PATCH only changed fields; never re-creates an existing product id " +
			"(Apple burns ids). Writes localizations on the open IAP version and uploads a review screenshot. " +
			"Never deletes locales.",
		Enums: map[string][]any{"type": iapTypes},
	}, iapUpsert))
}

type iapListIn struct {
	App      tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	State    string      `json:"state,omitempty"`
	Type     string      `json:"type,omitempty"`
	MaxItems int         `json:"max_items,omitempty" jsonschema:"default 200, max 5000"`
	Cursor   string      `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call"`
}

type iapLocOut struct {
	Locale      string `json:"locale"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type iapVersionOut struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
	State   string `json:"state,omitempty"`
}

type iapOut struct {
	ID             string         `json:"id"`
	ProductID      string         `json:"product_id"`
	ReferenceName  string         `json:"reference_name"`
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
		MaxItems: in.MaxItems,
		Cursor:   in.Cursor,
	})
	if err != nil {
		return nil, fmt.Errorf("list in-app purchases: %w", err)
	}
	inc := s12aIncluded(res.Included)
	out := iapListOut{App: app, Total: res.Total, Returned: res.Returned, ByState: map[string]int{}, Items: []iapOut{}, NextCursor: res.NextCursor}
	for _, r := range res.Data {
		fam, _ := r.Bool("familySharable")
		it := iapOut{
			ID: r.ID, ProductID: r.Str("productId"), ReferenceName: r.Str("name"),
			Type: r.Str("inAppPurchaseType"), State: r.Str("state"), FamilySharable: fam,
		}
		if v, ok := s12aLatestVersion(s12aRelated(r, "versions", "inAppPurchaseVersions", inc)); ok {
			n, _ := v.Int("version")
			it.Version = &iapVersionOut{ID: v.ID, Version: n, State: v.Str("state")}
		}
		it.Localizations = iapLocs(s12aRelated(r, "inAppPurchaseLocalizations", "inAppPurchaseLocalizations", inc))
		out.ByState[it.State]++
		out.Items = append(out.Items, it)
	}
	return tool.OK(out), nil
}

// iapLocs renders localizations sorted by locale.
func iapLocs(rs []tool.Resource) []iapLocOut {
	out := make([]iapLocOut, 0, len(rs))
	for _, l := range rs {
		out = append(out, iapLocOut{Locale: l.Str("locale"), Name: l.Str("name"), Description: l.Str("description")})
	}
	slices.SortFunc(out, func(a, b iapLocOut) int { return strings.Compare(a.Locale, b.Locale) })
	return out
}

type iapUpsertIn struct {
	App              tool.AppRef          `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	ProductID        string               `json:"product_id" jsonschema:"product identifier; the lookup key"`
	Type             string               `json:"type,omitempty" jsonschema:"required to create; cannot change later"`
	ReferenceName    string               `json:"reference_name,omitempty" jsonschema:"internal name (max 64); required to create"`
	ReviewNote       *string              `json:"review_note,omitempty"`
	FamilySharable   *bool                `json:"family_sharable,omitempty" jsonschema:"cannot be turned off once on"`
	Localizations    map[string]s12aLocIn `json:"localizations,omitempty" jsonschema:"by locale: display name (max 30) and description (max 45)"`
	ReviewScreenshot string               `json:"review_screenshot,omitempty" jsonschema:"path to a PNG/JPEG screenshot for App Review"`
}

type iapUpsertOut struct {
	App              tool.App        `json:"app"`
	Product          s12aItem        `json:"product"`
	ProductID        string          `json:"product_id"`
	Version          *s12aVersionOut `json:"version,omitempty"`
	Localizations    []s12aItem      `json:"localizations,omitempty"`
	ReviewScreenshot *s12aItem       `json:"review_screenshot,omitempty"`
	Failed           int             `json:"failed,omitempty"`
}

// iapProductFields are the sparse product fields upserts read back.
var iapProductFields = "name,productId,inAppPurchaseType,state,reviewNote,familySharable,appStoreReviewScreenshot"

func iapUpsert(ctx context.Context, env *tool.Env, in iapUpsertIn) (*tool.Result, error) {
	if err := s12aCheckProductID(in.ProductID); err != nil {
		return nil, err
	}
	if in.ReferenceName != "" {
		if err := s12aCheckLimit("reference_name", applemeta.FieldIAPReferenceName, in.ReferenceName); err != nil {
			return nil, err
		}
	}
	locs, err := s12aNormLocs("localizations", in.Localizations)
	if err != nil {
		return nil, err
	}
	if err := s12aCheckScreenshot(env, in.ReviewScreenshot); err != nil {
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

	out := iapUpsertOut{App: app, ProductID: in.ProductID}
	cur, shot, found, err := iapFind(ctx, env, app.ID, in.ProductID)
	if err != nil {
		return nil, err
	}
	if !found {
		if in.Type == "" || in.ReferenceName == "" {
			return nil, s12aInvalid("product %q does not exist; type and reference_name are required to create it", in.ProductID)
		}
		attrs := map[string]any{"productId": in.ProductID, "inAppPurchaseType": in.Type}
		for k, v := range want {
			attrs[k] = v
		}
		created, err := s12aPost(ctx, env, "/v2/inAppPurchases", "inAppPurchases", attrs, map[string][2]string{"app": {"apps", app.ID}})
		switch {
		case err == nil:
			cur, out.Product = created, s12aItem{Kind: "inAppPurchases", ID: created.ID, Action: s12aCreated}
		case s12aConflict(err):
			// Read back: the product exists (maybe created by a racing call).
			cur, shot, found, err = iapFind(ctx, env, app.ID, in.ProductID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("create in-app purchase %s: Apple reports a conflict but the product is not in this app; product ids are unique per account and cannot be reused: %w", in.ProductID, tool.ErrInvalidArgs)
			}
			out.Product = s12aItem{Kind: "inAppPurchases", ID: cur.ID, Action: s12aAlready}
		default:
			return nil, fmt.Errorf("create in-app purchase %s: %w", in.ProductID, err)
		}
	}
	if out.Product.Action != s12aCreated {
		if out.Product.Action == "" {
			out.Product = s12aItem{Kind: "inAppPurchases", ID: cur.ID, Action: s12aUnchanged}
		}
		if in.Type != "" && cur.Str("inAppPurchaseType") != "" && in.Type != cur.Str("inAppPurchaseType") {
			return nil, s12aInvalid("product %s is %s; the type of an in-app purchase cannot change", in.ProductID, cur.Str("inAppPurchaseType"))
		}
		changed, names, err := s12aDiff(cur, want)
		if err != nil {
			return nil, err
		}
		if len(changed) > 0 {
			if err := s12aPatch(ctx, env, "/v2/inAppPurchases/"+url.PathEscape(cur.ID), "inAppPurchases", cur.ID, changed); err != nil {
				return nil, fmt.Errorf("update in-app purchase %s: %w", in.ProductID, err)
			}
			out.Product.Action, out.Product.Changed = s12aUpdated, names
		}
	}

	items := []s12aItem{out.Product}
	if len(locs) > 0 {
		v, existing, err := s12aOpenVersion(ctx, env, s12aIAPVersions, cur.ID)
		if err != nil {
			return nil, err
		}
		out.Version = &v
		out.Localizations = s12aUpsertLocs(ctx, env, s12aIAPVersions, v.ID, existing, locs)
		items = append(items, out.Localizations...)
		if v.Created {
			items = append(items, s12aItem{Kind: "inAppPurchaseVersions", Action: s12aCreated})
		}
	}
	if in.ReviewScreenshot != "" {
		it := s12aScreenshot(ctx, env, s12aIAPShot, cur.ID, in.ReviewScreenshot, shot)
		out.ReviewScreenshot = &it
		items = append(items, it)
	}
	out.Failed = s12aFailures(items)
	if s12aAllUnchanged(items) {
		return tool.Already(out), nil
	}
	res := tool.OK(out).Then(
		tool.NextStep{Tool: "iap_list", Args: map[string]any{"app": app.ID}},
		tool.NextStep{Tool: "pricing_plan", Args: map[string]any{"app": app.ID}},
	)
	if out.Failed > 0 {
		res.Warn(strconv.Itoa(out.Failed) + " item(s) failed; see the error fields and rerun (the upsert is idempotent)")
	}
	return res, nil
}

// iapFind looks an IAP up by exact product id, with its review screenshot.
func iapFind(ctx context.Context, env *tool.Env, appID, productID string) (product, shot tool.Resource, found bool, err error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(appID)+"/inAppPurchasesV2", url.Values{
		"filter[productId]":      {productID},
		"include":                {"appStoreReviewScreenshot"},
		"fields[inAppPurchases]": {iapProductFields},
		"fields[inAppPurchaseAppStoreReviewScreenshots]": {s12aShotFields},
		"limit": {"200"},
	})
	if err != nil {
		return product, shot, false, fmt.Errorf("look up in-app purchase %s: %w", productID, err)
	}
	inc := s12aIncluded(resp.Included)
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
