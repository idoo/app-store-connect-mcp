package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "apps_create", Toolset: tool.Web, Class: tool.Destructive, Idempotent: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Create an app record",
		Description: "Create a new App Store Connect app record (web session). Optionally registers the bundle id first. " +
			"Irreversible: the name is reserved and the record cannot be deleted. Without confirm:true previews.",
		Enums: map[string][]any{"platforms": platformEnum},
	}, appsCreate))
}

type appsCreateIn struct {
	Name             string          `json:"name" jsonschema:"App Store name, at most 30 characters"`
	BundleID         string          `json:"bundle_id" jsonschema:"explicit bundle identifier"`
	SKU              string          `json:"sku,omitempty" jsonschema:"default: the bundle id"`
	Platforms        []tool.Platform `json:"platforms,omitempty" jsonschema:"default [IOS]"`
	PrimaryLocale    string          `json:"primary_locale,omitempty" jsonschema:"default en-US"`
	Version          string          `json:"version_string,omitempty" jsonschema:"first version string, default 1.0"`
	CompanyName      string          `json:"company_name,omitempty" jsonschema:"seller name; needed only for the account's first app"`
	RegisterBundleID bool            `json:"register_bundle_id,omitempty" jsonschema:"register the bundle id first when it is missing"`
	tool.ConfirmFlag
}

// createdApp is the app record apps_create created or found.
type createdApp struct {
	ID        string          `json:"id"`
	Name      tool.Untrusted  `json:"name"`
	BundleID  string          `json:"bundle_id"`
	SKU       string          `json:"sku,omitempty"`
	Platforms []tool.Platform `json:"platforms,omitempty"`
}

// createStep is one mutation of apps_create with its outcome: done,
// outcome_unknown, error or not_attempted.
type createStep struct {
	Step    string `json:"step"`
	Outcome string `json:"outcome"`
}

type appsCreateOut struct {
	App     createdApp   `json:"app"`
	Created bool         `json:"created"`
	Steps   []createStep `json:"steps,omitempty"`
}

// createPartial is the error data of an apps_create that failed after it
// may have changed something.
type createPartial struct {
	tool.ErrorBody
	Steps []createStep `json:"steps"`
}

const (
	stepRegisterBundle = "register bundle id"
	stepCreateApp      = "create app record"
	maxAppName         = 30
)

// skuRE is the alphabet Apple accepts for SKUs.
var skuRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// bundleRE is an explicit (non-wildcard) bundle identifier.
var bundleRE = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

// localeRE is a primary locale such as en-US, de-DE or zh-Hans.
var localeRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func normalizeCreate(in *appsCreateIn) error {
	in.Name = strings.TrimSpace(in.Name)
	in.BundleID = strings.TrimSpace(in.BundleID)
	in.SKU = strings.TrimSpace(in.SKU)
	in.PrimaryLocale = strings.TrimSpace(in.PrimaryLocale)
	in.Version = strings.TrimSpace(in.Version)
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0:
		return tool.InvalidArgs("name is required")
	case n > maxAppName:
		return tool.InvalidArgs("name has %d characters; the App Store allows at most %d", n, maxAppName)
	}
	if !bundleRE.MatchString(in.BundleID) || len(in.BundleID) > 155 {
		return tool.InvalidArgs("bundle_id %s is not an explicit bundle identifier", tool.SanitizeLine(in.BundleID, 160))
	}
	if in.SKU == "" {
		in.SKU = in.BundleID
	}
	if !skuRE.MatchString(in.SKU) {
		return tool.InvalidArgs("sku may contain only letters, digits, '.', '_' and '-' (at most 100)")
	}
	if in.PrimaryLocale == "" {
		in.PrimaryLocale = "en-US"
	}
	if !localeRE.MatchString(in.PrimaryLocale) {
		return tool.InvalidArgs("primary_locale %s is not a locale code such as en-US", tool.SanitizeLine(in.PrimaryLocale, 40))
	}
	if in.Version == "" {
		in.Version = "1.0"
	}
	if len(in.Platforms) == 0 {
		in.Platforms = []tool.Platform{tool.IOS}
	}
	seen := map[tool.Platform]bool{}
	var ps []tool.Platform
	for _, p := range in.Platforms {
		if !seen[p] {
			seen[p] = true
			ps = append(ps, p)
		}
	}
	in.Platforms = ps
	return nil
}

func appsCreate(ctx context.Context, env *tool.Env, in appsCreateIn) (*tool.Result, error) {
	if err := normalizeCreate(&in); err != nil {
		return nil, err
	}
	next := func(appID string) []tool.NextStep {
		return []tool.NextStep{
			{Tool: "apps_get", Args: map[string]any{"app": appID}},
			{Tool: "privacy_plan", Args: map[string]any{"app": appID}},
			{Tool: "release_prepare", Args: map[string]any{"app": appID}},
		}
	}
	existing, err := findApp(ctx, env, in.BundleID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if sameName(*existing, in.Name) {
			return tool.Already(appsCreateOut{App: viewApp(*existing, in)}).Then(next(existing.ID)...), nil
		}
		return nil, tool.WithNext(fmt.Errorf("%w: bundle id %s already belongs to app %s (%s); one app record per bundle id",
			tool.ErrInvalidArgs, in.BundleID, existing.ID, tool.SanitizeLine(string(existing.Text("name")), 60)),
			tool.NextStep{Tool: "apps_get", Args: map[string]any{"app": existing.ID}})
	}

	registered, regErr := bundleRegistered(ctx, env, in.BundleID)
	register := false
	var warnings []string
	switch {
	case regErr != nil && in.RegisterBundleID:
		return nil, fmt.Errorf("check bundle id %s: %w", in.BundleID, regErr)
	case regErr != nil:
		warnings = append(warnings, "could not check whether the bundle id is registered ("+tool.BodyFor(regErr).Error+"); Apple refuses the app if it is not")
	case !registered && !in.RegisterBundleID:
		return nil, tool.WithNext(tool.InvalidArgs("bundle id %s is not registered; pass register_bundle_id:true or register it first", in.BundleID),
			tool.NextStep{Tool: "signing_bundle_id_create", Args: map[string]any{"identifier": in.BundleID, "name": bundleName(in.Name), "platform": bundlePlatform(in.Platforms)}})
	case !registered:
		register = true
	}

	body := appCreateBody(in)
	var intended []string
	affected := []string{}
	if register {
		intended = append(intended, fmt.Sprintf("register bundle id %s (platform %s, name %s)", in.BundleID, bundlePlatform(in.Platforms), bundleName(in.Name)))
		affected = append(affected, "POST /v1/bundleIds "+in.BundleID)
	}
	intended = append(intended, fmt.Sprintf("create app record %q: bundle id %s, SKU %s, primary locale %s, platforms %s, first version %s",
		in.Name, in.BundleID, in.SKU, in.PrimaryLocale, joinPlatforms(in.Platforms), in.Version))
	if in.CompanyName != "" {
		intended = append(intended, "seller (company) name "+in.CompanyName+" (set once for the account)")
	}
	affected = append(affected, "POST appstoreconnect.apple.com/iris/v1/apps (web session)")
	preview := tool.PlanPreview{
		Summary:  fmt.Sprintf("create the App Store Connect app %q (%s)", in.Name, in.BundleID),
		Intended: intended,
		Affected: affected,
		Irreversible: []string{
			"an app record cannot be deleted, only removed from sale; the name stays reserved for this team",
			"the SKU and bundle id can never be changed or reused for another app",
		},
		Warnings: warnings,
	}
	wctx, res := tool.RequireConfirmTarget(ctx, env, in.Confirm, map[string]bool{"register_bundle_id": register}, preview)
	if res != nil {
		return res, nil
	}

	var steps []createStep
	fail := func(step string, err error) (*tool.Result, error) {
		steps = append(steps, createStep{Step: step, Outcome: outcomeOf(err)})
		if step == stepRegisterBundle {
			steps = append(steps, createStep{Step: stepCreateApp, Outcome: outcomeNotAttempted})
		}
		b := tool.BodyFor(err)
		return &tool.Result{Data: createPartial{ErrorBody: b, Steps: steps}, IsError: true, Next: b.Next}, nil
	}
	if register {
		if err := registerBundle(wctx, env, in); err != nil {
			return fail(stepRegisterBundle, err)
		}
		steps = append(steps, createStep{Step: stepRegisterBundle, Outcome: outcomeDone})
	}
	resp, err := env.Web.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/apps", Body: body, Write: true})
	if err != nil {
		if tool.IsConflict(err) {
			if app, ferr := findApp(ctx, env, in.BundleID); ferr == nil && app != nil && sameName(*app, in.Name) {
				steps = append(steps, createStep{Step: stepCreateApp, Outcome: outcomeDone})
				return tool.Already(appsCreateOut{App: viewApp(*app, in), Steps: steps}).Then(next(app.ID)...), nil
			}
		}
		return fail(stepCreateApp, explainCreateError(in, err))
	}
	r, ok := resp.First()
	if !ok || r.ID == "" {
		return fail(stepCreateApp, fmt.Errorf("create app %s: response has no app: %w", in.BundleID, tool.ErrOutcomeUnknown))
	}
	steps = append(steps, createStep{Step: stepCreateApp, Outcome: outcomeDone})
	return tool.OK(appsCreateOut{App: viewApp(r, in), Created: true, Steps: steps}).Then(next(r.ID)...), nil
}

// sameName reports whether the app is named name (case-insensitively), the
// only case apps_create treats as already done.
func sameName(r tool.Resource, name string) bool {
	return strings.EqualFold(strings.TrimSpace(string(r.Text("name"))), name)
}

// viewApp renders an app resource, falling back to the request for
// attributes the response omits.
func viewApp(r tool.Resource, in appsCreateIn) createdApp {
	a := createdApp{ID: r.ID, Name: r.Text("name"), BundleID: r.Str("bundleId"), SKU: r.Str("sku"), Platforms: in.Platforms}
	if a.Name == "" {
		a.Name = tool.Untrusted(in.Name)
	}
	if a.BundleID == "" {
		a.BundleID = in.BundleID
	}
	if a.SKU == "" {
		a.SKU = in.SKU
	}
	return a
}

// findApp returns the app with exactly this bundle id (public API), or nil.
func findApp(ctx context.Context, env *tool.Env, bundleID string) (*tool.Resource, error) {
	res, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Query:    url.Values{"filter[bundleId]": {bundleID}},
		Fields:   map[string][]string{"apps": {"name", "bundleId", "sku"}},
		MaxItems: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("look up apps with bundle id %s: %w", bundleID, err)
	}
	for _, a := range res.Data {
		if a.Str("bundleId") == bundleID {
			return &a, nil
		}
	}
	return nil, nil
}

// bundleRegistered reports whether the explicit identifier is registered
// (Apple's filter is a prefix match, so it is compared exactly).
func bundleRegistered(ctx context.Context, env *tool.Env, identifier string) (bool, error) {
	res, err := env.ASC.List(ctx, "/v1/bundleIds", tool.ListOptions{
		Query:    url.Values{"filter[identifier]": {identifier}},
		Fields:   map[string][]string{"bundleIds": {"identifier", "platform"}},
		MaxItems: 200,
	})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(res.Data, func(r tool.Resource) bool { return r.Str("identifier") == identifier }), nil
}

// registerBundle registers the bundle id; a 409 that reads back as
// registered counts as done.
func registerBundle(ctx context.Context, env *tool.Env, in appsCreateIn) error {
	body := map[string]any{"data": map[string]any{
		"type":       "bundleIds",
		"attributes": map[string]any{"identifier": in.BundleID, "name": bundleName(in.Name), "platform": bundlePlatform(in.Platforms)},
	}}
	_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/bundleIds", Body: body, Write: true})
	if tool.IsConflict(err) {
		if ok, rerr := bundleRegistered(ctx, env, in.BundleID); rerr == nil && ok {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("register bundle id %s: %w", in.BundleID, err)
	}
	return nil
}

// bundlePlatform maps app platforms to a bundle id platform: MAC_OS for
// Mac-only apps, IOS for iOS/tvOS/visionOS, UNIVERSAL for Mac plus others.
func bundlePlatform(ps []tool.Platform) string {
	mac := slices.Contains(ps, tool.MacOS)
	switch {
	case mac && len(ps) == 1:
		return "MAC_OS"
	case mac:
		return "UNIVERSAL"
	default:
		return "IOS"
	}
}

// bundleName is the developer-portal name: Apple accepts only letters,
// digits and spaces.
func bundleName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ') {
			b.WriteRune(r)
		}
	}
	if s := strings.Join(strings.Fields(b.String()), " "); s != "" {
		return s
	}
	return "App"
}

func joinPlatforms(ps []tool.Platform) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

// appCreateBody is the iris POST /v1/apps document: the app plus, as
// included resources with local ids, one appStoreVersion (and its
// primary-locale localization) per platform and the appInfo localization
// carrying the name.
func appCreateBody(in appsCreateIn) map[string]any {
	ref := func(typ, id string) map[string]string { return map[string]string{"type": typ, "id": id} }
	attrs := map[string]any{"sku": in.SKU, "primaryLocale": in.PrimaryLocale, "bundleId": in.BundleID}
	if in.CompanyName != "" {
		attrs["companyName"] = in.CompanyName
	}
	var versionRefs []map[string]string
	var included []any
	for _, p := range in.Platforms {
		vid, lid := "${new-appStoreVersion-"+string(p)+"}", "${new-appStoreVersionLocalization-"+string(p)+"}"
		versionRefs = append(versionRefs, ref("appStoreVersions", vid))
		included = append(included,
			map[string]any{
				"type": "appStoreVersions", "id": vid,
				"attributes":    map[string]any{"versionString": in.Version, "platform": string(p)},
				"relationships": map[string]any{"appStoreVersionLocalizations": map[string]any{"data": []any{ref("appStoreVersionLocalizations", lid)}}},
			},
			map[string]any{"type": "appStoreVersionLocalizations", "id": lid, "attributes": map[string]any{"locale": in.PrimaryLocale}},
		)
	}
	infoID, infoLocID := "${new-appInfo}", "${new-appInfoLocalization}"
	included = append(included,
		map[string]any{
			"type": "appInfos", "id": infoID,
			"relationships": map[string]any{"appInfoLocalizations": map[string]any{"data": []any{ref("appInfoLocalizations", infoLocID)}}},
		},
		map[string]any{"type": "appInfoLocalizations", "id": infoLocID, "attributes": map[string]any{"locale": in.PrimaryLocale, "name": in.Name}},
	)
	return map[string]any{
		"data": map[string]any{
			"type":       "apps",
			"attributes": attrs,
			"relationships": map[string]any{
				"appStoreVersions": map[string]any{"data": versionRefs},
				"appInfos":         map[string]any{"data": []any{ref("appInfos", infoID)}},
			},
		},
		"included": included,
	}
}

// explainCreateError turns Apple's create errors into a message naming the
// conflicting attribute. A duplicate 409 carries several errors[] entries
// (DUPLICATE, DUPLICATE, DUPLICATE.SAME_ACCOUNT), so every entry is read.
func explainCreateError(in appsCreateIn, err error) error {
	var api *tool.APIError
	if !errors.As(err, &api) {
		return fmt.Errorf("create app %s: %w", in.BundleID, err)
	}
	entries := append([]tool.APIError{*api}, api.More...)
	var conflicts []string
	dup, companyMissing := false, false
	add := func(s string) {
		if !slices.Contains(conflicts, s) {
			conflicts = append(conflicts, s)
		}
	}
	for _, e := range entries {
		code := strings.ToUpper(e.Code)
		text := strings.ToLower(e.Pointer + " " + e.Detail + " " + e.Title)
		if strings.Contains(text, "companyname") || strings.Contains(text, "company name") {
			companyMissing = true
		}
		if !strings.Contains(code, "DUPLICATE") {
			continue
		}
		dup = true
		switch {
		case strings.Contains(text, "sku"):
			add("SKU " + in.SKU + " was already used (SKUs can never be reused)")
		case strings.Contains(text, "bundle"):
			add("bundle id " + in.BundleID + " is already used by another app")
		case strings.Contains(text, "name"):
			add(fmt.Sprintf("the name %q is already taken", in.Name))
		case strings.HasSuffix(code, "DIFFERENT_ACCOUNT"):
			add(fmt.Sprintf("the name %q is taken by another developer", in.Name))
		case strings.HasSuffix(code, "SAME_ACCOUNT"):
			add("an app in this account already uses this name, SKU or bundle id")
		}
	}
	switch {
	case companyMissing:
		return fmt.Errorf("%w: Apple needs company_name (the seller name) for this account's first app: %w", tool.ErrInvalidArgs, err)
	case dup && len(conflicts) == 0:
		return fmt.Errorf("create app: the name %q, SKU %s or bundle id %s is already in use: %w", in.Name, in.SKU, in.BundleID, err)
	case dup:
		return fmt.Errorf("create app: %s: %w", strings.Join(conflicts, "; "), err)
	}
	return fmt.Errorf("create app %s: %w", in.BundleID, err)
}
