package release

import (
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

// Check statuses.
const (
	statusOK    = "ok"
	statusWarn  = "warn"
	statusBlock = "block"
)

// fixVia says how a check is fixed: a hamn tool, or manually at a URL.
type fixVia struct {
	Tool   string `json:"tool,omitempty"`
	Manual string `json:"manual,omitempty"`
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
	// AppInfoLocs are the editable appInfo localizations (name, subtitle, privacy URL).
	AppInfoLocs []tool.Resource
	AgeRating   *tool.Resource
	// Screenshots maps locale to its screenshot sets.
	Screenshots map[string][]screenshotSet
	// PriceSet / AvailabilitySet report whether a schedule/availability exists.
	PriceSet, AvailabilitySet bool
	Submission                *tool.Resource
	// IAPs: product name → version state (or product state when unversioned).
	IAPs map[string]string
	// Subscriptions: product name → state.
	Subscriptions map[string]string
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
	out = append(out, checkAgeRating(s), checkContentRights(s), checkPricing(s), checkProducts(s), checkPrivacy(s), checkSubmission(s))
	if s.Version != nil {
		out = append(out, checkPhased(s))
	}
	return out
}

func checkVersion(s *statusSnapshot) check {
	c := check{ID: "version"}
	if s.Version == nil {
		c.Status = statusBlock
		if concreteVersion(s.VersionRef) {
			c.Detail = fmt.Sprintf("version %s does not exist for %s", s.VersionRef, s.Platform)
		} else {
			c.Detail = fmt.Sprintf("no editable %s version; pass a version string to create one", s.Platform)
		}
		c.FixVia = fixTool("release_prepare")
		return c
	}
	state := s.Version.Version.Str("appVersionState")
	c.Detail = fmt.Sprintf("%s %s is %s", s.Platform, s.Version.Version.Str("versionString"), state)
	switch state {
	case "WAITING_FOR_REVIEW", "IN_REVIEW":
		c.Status, c.FixVia = statusWarn, fixTool("release_review_status")
		c.Detail += "; already submitted (cancel with release_cancel to edit)"
	case "PENDING_DEVELOPER_RELEASE":
		c.Status, c.FixVia = statusOK, fixTool("release_publish")
		c.Detail += "; approved, waiting for release_publish"
	case "READY_FOR_DISTRIBUTION", "PROCESSING_FOR_DISTRIBUTION", "PENDING_APPLE_RELEASE":
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
		} else {
			c.Status, c.Detail = statusOK, "build "+b.Str("version")+" attached and VALID"
		}
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
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusBlock, fmt.Sprintf("%d listing problem(s)", len(c.Items))
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
	primary := s.AppAttrs.Str("primaryLocale")
	warn := false
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
		c.Status, c.Detail = statusWarn, "some screenshots are still processing"
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

func checkAgeRating(s *statusSnapshot) check {
	c := check{ID: "age_rating", FixVia: fixTool("appinfo_update")}
	if s.AgeRating == nil {
		c.Status, c.Detail = statusBlock, "no age rating declaration"
		return c
	}
	for k, v := range s.AgeRating.Attributes {
		if v == nil && !slices.Contains(ageRatingOptional, k) {
			c.Items = append(c.Items, k)
		}
	}
	slices.Sort(c.Items)
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusWarn, fmt.Sprintf("%d age rating question(s) unanswered", len(c.Items))
		return c
	}
	c.Status, c.Detail, c.FixVia = statusOK, "age rating declaration complete", nil
	return c
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

func checkProducts(s *statusSnapshot) check {
	c := check{ID: "in_app_products", Status: statusOK}
	var ready, pending []string
	for name, st := range s.IAPs {
		switch st {
		case "READY_FOR_REVIEW":
			ready = append(ready, "iap "+name)
		case "PREPARE_FOR_SUBMISSION", "MISSING_METADATA", "DEVELOPER_ACTION_NEEDED", "REJECTED":
			pending = append(pending, "iap "+name+": "+st)
		}
	}
	for name, st := range s.Subscriptions {
		switch st {
		case "READY_TO_SUBMIT":
			ready = append(ready, "subscription "+name)
		case "MISSING_METADATA", "DEVELOPER_ACTION_NEEDED", "REJECTED":
			pending = append(pending, "subscription "+name+": "+st)
		}
	}
	slices.Sort(ready)
	slices.Sort(pending)
	c.Items = append(ready, pending...)
	switch {
	case len(pending) > 0:
		c.Status, c.FixVia = statusWarn, fixTool("iap_list")
		c.Detail = fmt.Sprintf("%d product(s) not ready; %d ready to attach to the submission", len(pending), len(ready))
	case len(ready) > 0:
		c.Detail = fmt.Sprintf("%d product(s) ready; release_submit attaches them", len(ready))
	default:
		c.Detail = "no products waiting for review"
	}
	return c
}

func checkPrivacy(s *statusSnapshot) check {
	c := check{ID: "app_privacy", Status: statusOK, FixVia: fixURL(ascURL(s.App.ID, "distribution/privacy"))}
	c.Detail = "not readable via the API; confirm App Privacy answers are current"
	if !s.PriorReleased {
		c.Status = statusWarn
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
		c.Status, c.FixVia = statusBlock, fixTool("release_cancel")
		c.Detail = "a submission is " + st + "; cancel it before submitting again"
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
