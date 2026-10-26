package release

import (
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Check statuses.
const (
	statusOK    = "ok"
	statusWarn  = "warn"
	statusBlock = "block"
	// statusInfo is a fact, not a problem: nothing to fix now.
	statusInfo = "info"
	// statusManual is a check app-store-connect-mcp cannot read via the API; a human must
	// confirm it in App Store Connect (fix_via.manual is the deep link).
	statusManual = "manual"
)

// lockedStates are AppVersionState values of a submitted-but-unreleased
// version: its build and metadata cannot change.
var lockedStates = []string{"WAITING_FOR_REVIEW", "IN_REVIEW", "ACCEPTED", "PENDING_DEVELOPER_RELEASE", "PENDING_APPLE_RELEASE", "PROCESSING_FOR_DISTRIBUTION"}

// versionScopedChecks cannot change while the version is with App Review.
var versionScopedChecks = []string{"build", "export_compliance", "whats_new", "listing", "screenshots", "review_detail"}

// fixVia says how a check is fixed: a app-store-connect-mcp tool, or manually at a URL.
type fixVia struct {
	Tool   string         `json:"tool,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Manual string         `json:"manual,omitempty"`
}

// check is one preflight result.
type check struct {
	ID     string   `json:"id"`
	Status string   `json:"status"`
	Detail string   `json:"detail"`
	Items  []string `json:"items,omitempty"`
	FixVia *fixVia  `json:"fix_via,omitempty"`
}

// screenshotSet is one display slot of a localization.
type screenshotSet struct {
	Display applemeta.DisplayType
	States  []string // assetDeliveryState.state per screenshot
}

// statusSnapshot is everything the preflight reads.
type statusSnapshot struct {
	App      tool.App
	Platform tool.Platform
	// AppAttrs are the app attributes (contentRightsDeclaration, primaryLocale).
	AppAttrs tool.Resource
	// PriorReleased reports a previous version reached the store (What's New
	// is required only then).
	PriorReleased bool
	// Version is nil when the requested version does not exist.
	Version    *versionDetail
	VersionRef tool.VersionRef
	// InFlight is the newest locked (submitted, unreleased) version of the
	// platform, from the app's included versions; nil when none.
	InFlight *tool.Resource
	// KoreaAvailable reports South Korea availability; nil when unknown
	// (only read when a Korea-only age rating question is unanswered).
	KoreaAvailable *bool
	// AppInfoLocs are the editable appInfo localizations (name, subtitle, privacy URL).
	AppInfoLocs []tool.Resource
	AgeRating   *tool.Resource
	// AgeTier / LiveAgeTier are appStoreAgeRating of the editable and the
	// live appInfo ("" when unknown).
	AgeTier, LiveAgeTier string
	// A11y are the app's accessibilityDeclarations; A11yErr is set when
	// they could not be read.
	A11y    []tool.Resource
	A11yErr string
	// Screenshots maps locale to its screenshot sets.
	Screenshots map[string][]screenshotSet
	// PriceSet / AvailabilitySet report whether a schedule/availability exists.
	PriceSet, AvailabilitySet bool
	Submission                *tool.Resource
	// BuildVersion is the marketing version (preReleaseVersion.version) of
	// the attached build; "" when unknown.
	BuildVersion string
	// IAPs: product name → version state (or product state when unversioned).
	IAPs map[string]string
	// Subscriptions: product name → state.
	Subscriptions map[string]string
	// ReadyIAPs / ReadySubscriptions are the product ids checkProducts lists
	// as ready; release_submit attaches them unless told not to.
	ReadyIAPs, ReadySubscriptions []string
	// ProductsTruncated describes product lists cut at the item cap.
	ProductsTruncated []string
}

func fixTool(name string) *fixVia { return &fixVia{Tool: name} }
func fixURL(u string) *fixVia     { return &fixVia{Manual: u} }

// runChecks evaluates every preflight check (pure).
func runChecks(s *statusSnapshot) []check {
	var out []check
	out = append(out, checkVersion(s))
	if s.Version != nil {
		out = append(out, checkBuild(s), checkExportCompliance(s), checkWhatsNew(s), checkListing(s), checkScreenshots(s), checkReviewDetail(s))
	}
	out = append(out, checkAgeRating(s), checkAccessibility(s), checkContentRights(s), checkPricing(s), checkProducts(s), checkPrivacy(s), checkSubmission(s))
	if s.Version != nil {
		out = append(out, checkPhased(s))
	}
	if st := s.lockedState(); st != "" {
		relabel(out, "locked while "+st+": ")
	} else if st := s.releasedState(); st != "" {
		relabel(out, "informational, "+st+" version is not editable: ")
	}
	return out
}

// relabel turns the version-scoped problems of a non-editable version into
// info, keeping their items (e.g. the missing screenshot slots per locale).
func relabel(out []check, prefix string) {
	for i, c := range out {
		if slices.Contains(versionScopedChecks, c.ID) && (c.Status == statusWarn || c.Status == statusBlock) {
			out[i].Status, out[i].FixVia = statusInfo, nil
			out[i].Detail = prefix + c.Detail
		}
	}
}

// releasedState returns the state of the inspected version when it already
// reached the store, else "".
func (s *statusSnapshot) releasedState() string {
	if s.Version == nil {
		return ""
	}
	if st := s.Version.Version.Str("appVersionState"); slices.Contains(releasedStates, st) {
		return st
	}
	return ""
}

// lockedState returns the state of the inspected version when it is
// submitted but unreleased, else "".
func (s *statusSnapshot) lockedState() string {
	if s.Version == nil {
		return ""
	}
	if st := s.Version.Version.Str("appVersionState"); slices.Contains(lockedStates, st) {
		return st
	}
	return ""
}

func checkVersion(s *statusSnapshot) check {
	c := check{ID: "version"}
	if s.Version == nil {
		switch {
		case concreteVersion(s.VersionRef):
			c.Status, c.FixVia = statusBlock, fixTool("release_prepare")
			c.Detail = fmt.Sprintf("version %s does not exist for %s", s.VersionRef, s.Platform)
		case s.InFlight != nil:
			c.Status = statusInfo
			c.Detail = fmt.Sprintf("no editable %s version: %s is %s (in flight); release_review_status tracks it", s.Platform, s.InFlight.Str("versionString"), s.InFlight.Str("appVersionState"))
		default:
			c.Status, c.FixVia = statusBlock, fixTool("release_prepare")
			c.Detail = fmt.Sprintf("no %s version in preparation or in flight; release_prepare with a version string creates one", s.Platform)
		}
		return c
	}
	state := s.Version.Version.Str("appVersionState")
	c.Detail = fmt.Sprintf("%s %s is %s", s.Platform, s.Version.Version.Str("versionString"), state)
	switch state {
	case "WAITING_FOR_REVIEW", "IN_REVIEW":
		c.Status = statusInfo
		c.Detail += "; submitted, waiting for Apple (release_review_status tracks it)"
	case "PENDING_DEVELOPER_RELEASE":
		c.Status, c.FixVia = statusOK, fixTool("release_publish")
		c.Detail += "; approved, waiting for release_publish"
	case "ACCEPTED", "PENDING_APPLE_RELEASE", "PROCESSING_FOR_DISTRIBUTION":
		c.Status = statusInfo
		c.Detail += "; approved, Apple is releasing it"
	case "READY_FOR_DISTRIBUTION", "REPLACED_WITH_NEW_VERSION", "DEVELOPER_REMOVED_FROM_SALE", "REMOVED_FROM_SALE":
		c.Status = statusWarn
		c.Detail += "; already released, create a new version to ship changes"
	default:
		c.Status = statusOK
	}
	return c
}

func (s *statusSnapshot) build() (tool.Resource, bool) {
	return s.Version.one("build", "builds")
}

func checkBuild(s *statusSnapshot) check {
	c := check{ID: "build"}
	b, ok := s.build()
	switch {
	case !ok:
		c.Status, c.Detail, c.FixVia = statusBlock, "no build attached", fixTool("release_prepare")
	case b.Str("processingState") == "PROCESSING":
		c.Status, c.Detail, c.FixVia = statusWarn, "build "+b.Str("version")+" is still processing", fixTool("builds_wait")
	case b.Str("processingState") != "VALID":
		c.Status, c.Detail, c.FixVia = statusBlock, "build "+b.Str("version")+" is "+b.Str("processingState"), fixTool("release_prepare")
	default:
		if exp, _ := b.Bool("expired"); exp {
			c.Status, c.Detail, c.FixVia = statusBlock, "build "+b.Str("version")+" is expired", fixTool("release_prepare")
			break
		}
		want := s.Version.Version.Str("versionString")
		if s.BuildVersion != "" && want != "" && s.BuildVersion != want {
			c.Status, c.FixVia = statusBlock, fixTool("release_prepare")
			c.Detail = fmt.Sprintf("build %s belongs to version %s, not %s; attach a %s build (build: \"latest\" picks the newest one)", b.Str("version"), s.BuildVersion, want, want)
			break
		}
		c.Status, c.Detail = statusOK, "build "+b.Str("version")+" attached and VALID"
	}
	return c
}

func checkExportCompliance(s *statusSnapshot) check {
	c := check{ID: "export_compliance"}
	b, ok := s.build()
	if !ok {
		c.Status, c.Detail, c.FixVia = statusBlock, "no build to answer export compliance for", fixTool("release_prepare")
		return c
	}
	v, set := b.Bool("usesNonExemptEncryption")
	switch {
	case !set || s.Version.Version.Str("appVersionState") == "WAITING_FOR_EXPORT_COMPLIANCE":
		c.Status, c.Detail, c.FixVia = statusBlock, "export compliance unanswered (usesNonExemptEncryption is null)", fixTool("release_prepare")
	case v:
		c.Status, c.Detail = statusWarn, "build uses non-exempt encryption; an approved encryption declaration must be linked"
		c.FixVia = fixURL(ascURL(s.App.ID, "distribution/info"))
	default:
		c.Status, c.Detail = statusOK, "exempt encryption declared"
	}
	return c
}

func checkWhatsNew(s *statusSnapshot) check {
	c := check{ID: "whats_new", Status: statusOK}
	if !s.PriorReleased {
		c.Detail = "first version: What's New is not required (and cannot be set)"
		return c
	}
	for _, l := range s.Version.localizations() {
		if strings.TrimSpace(l.Str("whatsNew")) == "" {
			c.Items = append(c.Items, l.Str("locale"))
		}
	}
	if len(c.Items) > 0 {
		c.Status, c.FixVia = statusBlock, fixTool("release_prepare")
		c.Detail = fmt.Sprintf("What's New missing in %d locale(s)", len(c.Items))
	} else {
		c.Detail = "What's New set in every locale"
	}
	return c
}

func limitIssue(locale string, f applemeta.Field, v string) string {
	if err := applemeta.Check(f, v); err != nil {
		return locale + ": " + err.Error()
	}
	return ""
}

func checkListing(s *statusSnapshot) check {
	c := check{ID: "listing", Status: statusOK, FixVia: fixTool("metadata_plan")}
	for _, l := range s.Version.localizations() {
		loc := l.Str("locale")
		for _, req := range []struct{ attr, label string }{{"description", "description"}, {"keywords", "keywords"}, {"supportUrl", "support URL"}} {
			if strings.TrimSpace(l.Str(req.attr)) == "" {
				c.Items = append(c.Items, loc+": "+req.label+" missing")
			}
		}
		for f, attr := range map[applemeta.Field]string{applemeta.FieldKeywords: "keywords", applemeta.FieldPromotionalText: "promotionalText", applemeta.FieldDescription: "description", applemeta.FieldWhatsNew: "whatsNew"} {
			if msg := limitIssue(loc, f, l.Str(attr)); msg != "" {
				c.Items = append(c.Items, msg)
			}
		}
	}
	for _, l := range s.AppInfoLocs {
		loc := l.Str("locale")
		for f, attr := range map[applemeta.Field]string{applemeta.FieldName: "name", applemeta.FieldSubtitle: "subtitle"} {
			if msg := limitIssue(loc, f, l.Str(attr)); msg != "" {
				c.Items = append(c.Items, msg)
			}
		}
		if strings.TrimSpace(l.Str("name")) == "" {
			c.Items = append(c.Items, loc+": name missing")
		}
		if strings.TrimSpace(l.Str("privacyPolicyUrl")) == "" {
			c.Items = append(c.Items, loc+": privacy policy URL missing")
		}
	}
	slices.Sort(c.Items)
	c.FixVia.Args = map[string]any{"app": s.App.ID, "platform": string(s.Platform)}
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusBlock, fmt.Sprintf("%d listing problem(s); first: %s", len(c.Items), c.Items[0])
	} else {
		c.Detail, c.FixVia = "every locale has the required fields within limits", nil
	}
	return c
}

func checkScreenshots(s *statusSnapshot) check {
	c := check{ID: "screenshots", Status: statusOK, FixVia: fixTool("screenshots_plan")}
	supportsIPad := false
	for _, sets := range s.Screenshots {
		for _, set := range sets {
			if strings.HasPrefix(string(set.Display), "APP_IPAD") {
				supportsIPad = true
			}
		}
	}
	required := applemeta.RequiredDisplays(s.Platform, supportsIPad)
	// The API does not say whether a build runs on iPad, so an iOS version
	// with no iPad screenshots anywhere is never reported as covered.
	ipadUnknown := s.Platform == tool.IOS && !supportsIPad
	if ipadUnknown {
		ipad := applemeta.RequiredDisplays(s.Platform, true)
		if len(ipad) > len(required) {
			c.Items = append(c.Items, fmt.Sprintf("%s: no iPad screenshots; if the build supports iPad, %s is required", s.AppAttrs.Str("primaryLocale"), joinDisplays(ipad[len(ipad)-1])))
		}
	}
	primary := s.AppAttrs.Str("primaryLocale")
	warn := ipadUnknown && len(c.Items) > 0
	for _, l := range s.Version.localizations() {
		loc := l.Str("locale")
		has := map[applemeta.DisplayType]bool{}
		for _, set := range s.Screenshots[loc] {
			if len(set.States) > 0 {
				has[set.Display] = true
			}
			for _, st := range set.States {
				switch st {
				case "COMPLETE":
				case "FAILED":
					c.Items = append(c.Items, fmt.Sprintf("%s: %s has a FAILED screenshot", loc, set.Display))
					c.Status = statusBlock
				default:
					c.Items = append(c.Items, fmt.Sprintf("%s: %s has a screenshot in state %s", loc, set.Display, st))
					warn = true
				}
			}
		}
		if loc != primary && len(has) == 0 {
			continue // falls back to the primary locale's screenshots
		}
		for _, group := range required {
			if !slices.ContainsFunc(group, func(d applemeta.DisplayType) bool { return has[d] }) {
				c.Items = append(c.Items, fmt.Sprintf("%s: no screenshots for %s", loc, joinDisplays(group)))
				c.Status = statusBlock
			}
		}
	}
	slices.Sort(c.Items)
	switch {
	case c.Status == statusBlock:
		c.Detail = "required screenshots missing or failed"
	case warn:
		c.Status, c.Detail = statusWarn, "some screenshots are still processing or iPad support is unknown"
	default:
		c.Detail, c.FixVia = "required display types covered", nil
	}
	return c
}

func joinDisplays(g []applemeta.DisplayType) string {
	parts := make([]string, len(g))
	for i, d := range g {
		parts[i] = string(d)
	}
	return strings.Join(parts, " or ")
}

func checkReviewDetail(s *statusSnapshot) check {
	c := check{ID: "review_detail", FixVia: fixTool("appinfo_update")}
	d, ok := s.Version.one("appStoreReviewDetail", "appStoreReviewDetails")
	if !ok {
		c.Status, c.Detail = statusBlock, "no App Review contact for this version"
		return c
	}
	for _, a := range []string{"contactFirstName", "contactLastName", "contactPhone", "contactEmail"} {
		if strings.TrimSpace(d.Str(a)) == "" {
			c.Items = append(c.Items, a+" missing")
		}
	}
	if req, _ := d.Bool("demoAccountRequired"); req && (d.Str("demoAccountName") == "" || d.Str("demoAccountPassword") == "") {
		c.Items = append(c.Items, "demo account required but name/password missing")
	}
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusBlock, "App Review contact incomplete"
		return c
	}
	c.Status, c.Detail, c.FixVia = statusOK, "App Review contact set", nil
	if req, set := d.Bool("demoAccountRequired"); !set || !req {
		c.Detail += "; no demo account (add one if the app needs a login)"
	}
	return c
}

// ageRatingOptional are declaration attributes that may legitimately be null.
var ageRatingOptional = []string{"kidsAgeBand", "ageRatingOverride", "ageRatingOverrideV2", "koreaAgeRatingOverride", "developerAgeRatingInfoUrl"}

// koreaOnly reports a declaration attribute required only for distribution
// in South Korea (GRAC).
func koreaOnly(attr string) bool {
	a := strings.ToLower(attr)
	return strings.HasPrefix(a, "grac") || strings.HasPrefix(a, "korea")
}

// unansweredKorea lists the unanswered Korea-only questions of decl.
func unansweredKorea(decl *tool.Resource) []string {
	var out []string
	if decl == nil {
		return nil
	}
	for k, v := range decl.Attributes {
		if v == nil && koreaOnly(k) && !slices.Contains(ageRatingOptional, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func checkAgeRating(s *statusSnapshot) check {
	c := check{ID: "age_rating", FixVia: fixTool("appinfo_update")}
	if s.AgeRating == nil {
		c.Status, c.Detail = statusBlock, "no age rating declaration"
		return c
	}
	general := 0
	for k, v := range s.AgeRating.Attributes {
		if v == nil && !slices.Contains(ageRatingOptional, k) {
			c.Items = append(c.Items, k)
			if !koreaOnly(k) {
				general++
			}
		}
	}
	slices.Sort(c.Items)
	korea := len(c.Items) - general
	semantic := ageRatingSemantics(s.AgeRating)
	switch {
	case general > 0:
		c.Status, c.Detail = statusBlock, fmt.Sprintf("%d age rating question(s) unanswered; Apple refuses the submission until they are answered", general)
	case len(semantic) > 0:
		c.Status, c.Detail = statusWarn, "age rating answers are inconsistent with the 2025 questionnaire"
	case korea > 0 && s.KoreaAvailable != nil && *s.KoreaAvailable:
		c.Status, c.Detail = statusWarn, fmt.Sprintf("%d Korea (GRAC) age rating question(s) unanswered; required because the app is available in South Korea", korea)
	case korea > 0:
		c.Status, c.FixVia = statusInfo, nil
		c.Detail = fmt.Sprintf("%d Korea (GRAC) age rating question(s) unanswered; needed only if the app is offered in South Korea, which it is not", korea)
		if s.KoreaAvailable == nil {
			c.Detail = fmt.Sprintf("%d Korea (GRAC) age rating question(s) unanswered; needed only for South Korea (availability unknown)", korea)
		}
	default:
		c.Status, c.Detail, c.FixVia = statusOK, "age rating declaration complete", nil
	}
	c.Items = append(c.Items, semantic...)
	if s.AgeTier != "" && s.LiveAgeTier != "" && s.AgeTier != s.LiveAgeTier {
		c.Detail += fmt.Sprintf("; note: rating changes from %s (live) to %s", s.LiveAgeTier, s.AgeTier)
	}
	return c
}

// ageRatingSemantics lists 2025 questionnaire answers that contradict each
// other (the null check catches unanswered ones).
func ageRatingSemantics(decl *tool.Resource) []string {
	var out []string
	noURL := decl.Str("developerAgeRatingInfoUrl") == ""
	for _, k := range []string{"ageAssurance", "socialMediaAgeRestricted"} {
		if v, _ := decl.Bool(k); v && noURL {
			out = append(out, k+" is true but developerAgeRatingInfoUrl is empty")
		}
	}
	if decl.Str("ageRatingOverride") != "" && decl.Attributes["ageRatingOverrideV2"] == nil {
		out = append(out, "legacy ageRatingOverride set but ageRatingOverrideV2 empty: re-answer the 2025 questionnaire")
	}
	return out
}

func checkContentRights(s *statusSnapshot) check {
	if s.AppAttrs.Str("contentRightsDeclaration") == "" {
		return check{ID: "content_rights", Status: statusBlock, Detail: "content rights not declared", FixVia: fixTool("appinfo_update")}
	}
	return check{ID: "content_rights", Status: statusOK, Detail: s.AppAttrs.Str("contentRightsDeclaration")}
}

func checkPricing(s *statusSnapshot) check {
	c := check{ID: "pricing_availability", FixVia: fixURL(ascURL(s.App.ID, "distribution/pricing"))}
	if !s.PriceSet {
		c.Items = append(c.Items, "no price schedule")
	}
	if !s.AvailabilitySet {
		c.Items = append(c.Items, "no territory availability")
	}
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusBlock, "pricing and availability not set"
		return c
	}
	c.Status, c.Detail, c.FixVia = statusOK, "price schedule and availability set", nil
	return c
}

// Product readiness by state. IAP states are the version state when the
// product has versions, else InAppPurchaseState; subscriptions use
// Subscription.state.
var (
	iapReady       = []string{"READY_FOR_REVIEW", "READY_TO_SUBMIT"}
	iapPending     = []string{"PREPARE_FOR_SUBMISSION", "MISSING_METADATA", "WAITING_FOR_UPLOAD", "DEVELOPER_ACTION_NEEDED", "REJECTED", "DEVELOPER_REJECTED"}
	subReady       = []string{"READY_TO_SUBMIT"}
	subPending     = []string{"MISSING_METADATA", "DEVELOPER_ACTION_NEEDED", "REJECTED"}
	productPending = map[bool][]string{true: iapPending, false: subPending}
)

func checkProducts(s *statusSnapshot) check {
	c := check{ID: "in_app_products", Status: statusOK}
	var ready, pending []string
	for name, st := range s.IAPs {
		switch {
		case slices.Contains(iapReady, st):
			ready = append(ready, "iap "+name)
		case slices.Contains(productPending[true], st):
			pending = append(pending, "iap "+name+": "+st)
		}
	}
	for name, st := range s.Subscriptions {
		switch {
		case slices.Contains(subReady, st):
			ready = append(ready, "subscription "+name)
		case slices.Contains(productPending[false], st):
			pending = append(pending, "subscription "+name+": "+st)
		}
	}
	slices.Sort(ready)
	slices.Sort(pending)
	c.Items = append(ready, pending...)
	switch {
	case len(pending) > 0:
		c.Status, c.FixVia = statusWarn, fixTool("iap_list")
		c.Detail = fmt.Sprintf("%d product(s) not ready; %d ready, release_submit attaches those", len(pending), len(ready))
	case len(ready) > 0:
		c.Detail = fmt.Sprintf("%d product(s) ready; release_submit attaches them (attach_first_time_products:false skips them)", len(ready))
	default:
		c.Detail = "no products waiting for review"
	}
	if len(s.ProductsTruncated) > 0 {
		// A partial list may hide first-time products: never report "ready" from it.
		c.Status, c.FixVia = statusWarn, fixTool("iap_list")
		c.Detail = strings.Join(s.ProductsTruncated, "; ") + "; " + c.Detail
	}
	return c
}

func checkPrivacy(s *statusSnapshot) check {
	c := check{ID: "app_privacy", Status: statusManual, FixVia: fixURL(ascURL(s.App.ID, "distribution/privacy"))}
	c.Detail = "not readable via the API; confirm App Privacy answers are current in App Store Connect"
	if !s.PriorReleased {
		c.Detail = "first release: App Privacy must be published in App Store Connect (not available via the API)"
	}
	return c
}

func checkSubmission(s *statusSnapshot) check {
	c := check{ID: "submission", Status: statusOK, Detail: "no review submission in flight"}
	if s.Submission == nil {
		return c
	}
	st := s.Submission.Str("state")
	switch st {
	case "WAITING_FOR_REVIEW", "IN_REVIEW":
		if s.Version == nil || s.lockedState() != "" {
			c.Status = statusInfo
			c.Detail = "a submission is " + st + ", waiting for Apple; release_review_status tracks it"
			return c
		}
		// An editable version while another submission is with Apple: a new
		// submission must wait. Cancelling is the user's call, never a fix.
		c.Status, c.FixVia = statusBlock, fixTool("release_review_status")
		c.Detail = "another submission is " + st + "; release_submit must wait until Apple finishes it (release_cancel would withdraw it and lose its queue place, only if the user asks)"
	case "UNRESOLVED_ISSUES":
		c.Status, c.FixVia = statusWarn, fixURL(ascURL(s.App.ID, "distribution/reviewsubmissions"))
		c.Detail = "a submission has unresolved issues; fix them, then release_submit resubmits it"
	default:
		c.Detail = "draft submission (" + st + ") will be reused by release_submit"
	}
	return c
}

func checkPhased(s *statusSnapshot) check {
	c := check{ID: "phased_release", Status: statusOK}
	p, ok := s.Version.one("appStoreVersionPhasedRelease", "appStoreVersionPhasedReleases")
	if !ok {
		c.Detail = "not enabled (immediate release to all users)"
		return c
	}
	c.Detail = "phased release " + p.Str("phasedReleaseState")
	if day, ok := p.Int("currentDayNumber"); ok && day > 0 {
		c.Detail += fmt.Sprintf(", day %d of 7", day)
	}
	return c
}
