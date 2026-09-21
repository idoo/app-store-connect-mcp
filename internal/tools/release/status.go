package release

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

type statusIn struct {
	App      tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform   `json:"platform,omitempty"`
	Version  tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default), live or a version string"`
	Desired  *releaseDesired `json:"desired,omitempty" jsonschema:"intended release; yields plan_ids for release_prepare/release_submit"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_status", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
		Title: "Release readiness and preflight",
		Description: "Preflight a version: build, compliance, What's New, listing limits, screenshots, review contact, age " +
			"rating, pricing, products, open submission. desired returns plan_ids for " +
			"release_prepare/release_submit.",
		Enums: map[string][]any{
			"platform":             {"IOS", "MAC_OS", "TV_OS", "VISION_OS"},
			"desired.release_type": {releaseManual, releaseAfterApproval, releaseScheduled},
		},
	}, releaseStatus))
}

type statusVersion struct {
	ID              string        `json:"id,omitempty"`
	String          string        `json:"string"`
	AppVersionState string        `json:"app_version_state,omitempty"`
	Platform        tool.Platform `json:"platform"`
	ReleaseType     string        `json:"release_type,omitempty"`
	Exists          bool          `json:"exists"`
}

// statusOption is an optional follow-up the user may choose; it is never a
// fix and never a `next` step.
type statusOption struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args,omitempty"`
	Reason string         `json:"reason"`
	// Locked/Unlock mirror tool.NextStep: set when Tool is hidden because
	// writes are disabled.
	Locked string         `json:"locked,omitempty"`
	Unlock *tool.NextStep `json:"unlock,omitempty"`
}

// statusInFlight is a submitted-but-unreleased version reported when no
// editable version exists.
type statusInFlight struct {
	ID              string         `json:"id"`
	String          string         `json:"string"`
	AppVersionState string         `json:"app_version_state"`
	Options         []statusOption `json:"options,omitempty"`
}

type statusBuild struct {
	ID                      string `json:"id"`
	Number                  string `json:"number"`
	ProcessingState         string `json:"processing_state"`
	Expired                 bool   `json:"expired,omitempty"`
	UsesNonExemptEncryption *bool  `json:"uses_non_exempt_encryption"`
}

// Release phases (statusSummary.Phase).
const (
	phasePreparing      = "preparing"
	phaseMissing        = "no_version"
	phaseInReview       = "in_review"
	phasePendingRelease = "pending_release"
	phaseReleased       = "released"
)

type statusSummary struct {
	OK     int    `json:"ok"`
	Warn   int    `json:"warn"`
	Block  int    `json:"block"`
	Info   int    `json:"info,omitempty"`
	Manual int    `json:"manual,omitempty"`
	Phase  string `json:"phase"`
	// Ready (no blocks) is reported only while a version is being prepared.
	Ready *bool `json:"ready,omitempty"`
}

type statusPlan struct {
	Changes        []string `json:"changes"`
	SkippedLocales []string `json:"skipped_locales,omitempty"`
	Notes          []string `json:"notes,omitempty"`
	PreparePlanID  string   `json:"prepare_plan_id,omitempty"`
	SubmitPlanID   string   `json:"submit_plan_id,omitempty"`
}

type statusOut struct {
	App          tool.App        `json:"app"`
	Version      statusVersion   `json:"version"`
	InFlight     *statusInFlight `json:"in_flight,omitempty"`
	Build        *statusBuild    `json:"build,omitempty"`
	Checks       []check         `json:"checks"`
	Submission   map[string]any  `json:"submission,omitempty"`
	PhasedState  string          `json:"phased_release,omitempty"`
	Summary      statusSummary   `json:"summary"`
	Plan         *statusPlan     `json:"plan,omitempty"`
	AppStoreLink string          `json:"app_store_connect"`
}

func releaseStatus(ctx context.Context, env *tool.Env, in statusIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	ref := in.Version
	if ref == "" {
		ref = tool.VersionEditable
	}
	s, err := loadStatusSnapshot(ctx, env, app, platform, ref)
	if err != nil {
		return nil, err
	}
	out := statusOut{App: app, Checks: runChecks(s), AppStoreLink: versionURL(app.ID, platform)}
	out.Version = statusVersion{String: string(ref), Platform: platform}
	if s.Version != nil {
		v := s.Version.Version
		out.Version = statusVersion{ID: v.ID, String: v.Str("versionString"), AppVersionState: v.Str("appVersionState"), Platform: platform, ReleaseType: v.Str("releaseType"), Exists: true}
		if b, ok := s.build(); ok {
			out.Build = &statusBuild{ID: b.ID, Number: b.Str("version"), ProcessingState: b.Str("processingState")}
			out.Build.Expired, _ = b.Bool("expired")
			if v, set := b.Bool("usesNonExemptEncryption"); set {
				out.Build.UsesNonExemptEncryption = &v
			}
		}
		if p, ok := s.Version.one("appStoreVersionPhasedRelease", "appStoreVersionPhasedReleases"); ok {
			out.PhasedState = p.Str("phasedReleaseState")
		}
	}
	if s.Submission != nil {
		out.Submission = map[string]any{"id": s.Submission.ID, "state": s.Submission.Str("state")}
	}
	for _, c := range out.Checks {
		switch c.Status {
		case statusOK:
			out.Summary.OK++
		case statusWarn:
			out.Summary.Warn++
		case statusInfo:
			out.Summary.Info++
		case statusManual:
			out.Summary.Manual++
		default:
			out.Summary.Block++
		}
	}
	out.Summary.Phase = releasePhase(s)
	if out.Summary.Phase == phasePreparing {
		ready := out.Summary.Block == 0
		out.Summary.Ready = &ready
	}
	res := tool.OK(&out)
	reviewNext := tool.NextStep{Tool: "release_review_status", Args: map[string]any{"app": app.ID, "platform": string(platform)}}
	if s.Version == nil && !concreteVersion(ref) && s.InFlight != nil {
		f := s.InFlight
		out.InFlight = &statusInFlight{ID: f.ID, String: f.Str("versionString"), AppVersionState: f.Str("appVersionState")}
		if slices.Contains([]string{"WAITING_FOR_REVIEW", "IN_REVIEW"}, out.InFlight.AppVersionState) {
			opt := statusOption{
				Tool: "release_cancel", Args: map[string]any{"app": app.ID, "platform": string(platform)},
				Reason: "only if the user wants to change " + out.InFlight.String + " before review; withdrawing loses its place in the review queue",
			}
			if !env.Profile.AllowWrites {
				opt.Locked = "writes_disabled"
				opt.Unlock = &tool.NextStep{Tool: "toolsets", Args: map[string]any{"action": "enable_writes"}}
			}
			out.InFlight.Options = []statusOption{opt}
		}
		res.Then(reviewNext)
	} else if out.Summary.Phase == phaseInReview {
		res.Then(reviewNext)
	}
	if in.Desired != nil {
		plan, next, err := planRelease(ctx, env, s, in)
		if err != nil {
			return nil, err
		}
		out.Plan = plan
		res.Then(next...)
	}
	for _, c := range out.Checks {
		if (c.Status == statusWarn || c.Status == statusBlock) && c.FixVia != nil && c.FixVia.Tool != "" && in.Desired == nil {
			res.Then(tool.NextStep{Tool: c.FixVia.Tool, Args: map[string]any{"app": app.ID, "platform": string(platform)}})
			break
		}
	}
	return res, nil
}

// releasePhase classifies where the inspected version is in its lifecycle.
func releasePhase(s *statusSnapshot) string {
	if s.Version == nil {
		if !concreteVersion(s.VersionRef) && s.InFlight != nil {
			return phaseFor(s.InFlight.Str("appVersionState"))
		}
		return phaseMissing
	}
	return phaseFor(s.Version.Version.Str("appVersionState"))
}

func phaseFor(state string) string {
	switch {
	case state == "WAITING_FOR_REVIEW" || state == "IN_REVIEW":
		return phaseInReview
	case slices.Contains(lockedStates, state):
		return phasePendingRelease
	case slices.Contains(releasedStates, state):
		return phaseReleased
	default:
		return phasePreparing
	}
}

// loadStatusSnapshot reads everything the preflight needs: 8 GETs plus one
// per version localization (screenshot sets).
func loadStatusSnapshot(ctx context.Context, env *tool.Env, app tool.App, platform tool.Platform, ref tool.VersionRef) (*statusSnapshot, error) {
	s := &statusSnapshot{App: app, Platform: platform, VersionRef: ref, Screenshots: map[string][]screenshotSet{}, IAPs: map[string]string{}, Subscriptions: map[string]string{}}

	appResp, err := env.ASC.Get(ctx, "/v1/apps/"+app.ID, url.Values{
		"fields[apps]":             {"primaryLocale,contentRightsDeclaration,appStoreVersions"},
		"include":                  {"appStoreVersions"},
		"limit[appStoreVersions]":  {"50"},
		"fields[appStoreVersions]": {"platform,versionString,appVersionState,releaseType"},
	})
	if err != nil {
		return nil, fmt.Errorf("get app: %w", err)
	}
	s.AppAttrs, _ = appResp.First()
	for _, v := range appResp.Included {
		if v.Type != "appStoreVersions" || v.Str("platform") != string(platform) {
			continue
		}
		st := v.Str("appVersionState")
		if slices.Contains(releasedStates, st) {
			s.PriorReleased = true
		}
		if slices.Contains(lockedStates, st) && (s.InFlight == nil || newerVersion(v.Str("versionString"), s.InFlight.Str("versionString"))) {
			s.InFlight = &v
		}
	}

	if ver, err := env.Resolve.Version(ctx, app.ID, platform, ref); err == nil {
		if s.Version, err = fetchVersionDetail(ctx, env, ver.ID); err != nil {
			return nil, err
		}
		if b, ok := s.build(); ok {
			if s.BuildVersion, err = buildMarketingVersion(ctx, env, b.ID); err != nil {
				return nil, err
			}
		}
	} else if !isNotFound(err) {
		return nil, err
	}

	if err := loadAppInfo(ctx, env, s); err != nil {
		return nil, err
	}
	if s.Version != nil {
		for _, l := range s.Version.localizations() {
			sets, err := fetchScreenshotSets(ctx, env, l.ID)
			if err != nil {
				return nil, err
			}
			s.Screenshots[l.Str("locale")] = sets
		}
	}
	if s.PriceSet, err = exists(ctx, env, "/v1/apps/"+app.ID+"/appPriceSchedule", url.Values{"include": {"baseTerritory"}}); err != nil {
		return nil, err
	}
	availID, err := availabilityID(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	s.AvailabilitySet = availID != ""
	if availID != "" && len(unansweredKorea(s.AgeRating)) > 0 {
		if s.KoreaAvailable, err = koreaAvailable(ctx, env, availID); err != nil {
			return nil, err
		}
	}
	if sub, ok, err := fetchOpenSubmission(ctx, env, app.ID, platform); err != nil {
		return nil, err
	} else if ok {
		s.Submission = &sub
	}
	if err := loadProducts(ctx, env, s); err != nil {
		return nil, err
	}
	return s, nil
}

// releasedStates mark a version that reached the store.
var releasedStates = []string{"READY_FOR_DISTRIBUTION", "REPLACED_WITH_NEW_VERSION", "DEVELOPER_REMOVED_FROM_SALE", "REMOVED_FROM_SALE"}

func loadAppInfo(ctx context.Context, env *tool.Env, s *statusSnapshot) error {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+s.App.ID+"/appInfos", url.Values{
		"include":                     {"appInfoLocalizations,ageRatingDeclaration"},
		"limit[appInfoLocalizations]": {"50"},
	})
	if err != nil {
		return fmt.Errorf("list app infos: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil
	}
	info := resp.Data[0]
	for _, i := range resp.Data {
		if st := i.Str("state"); st != "READY_FOR_DISTRIBUTION" && st != "REPLACED_WITH_NEW_INFO" {
			info = i
			break
		}
	}
	locIDs := info.RelIDs("appInfoLocalizations")
	ageID := info.RelID("ageRatingDeclaration")
	for _, r := range resp.Included {
		switch {
		case r.Type == "appInfoLocalizations" && slices.Contains(locIDs, r.ID):
			s.AppInfoLocs = append(s.AppInfoLocs, r)
		case r.Type == "ageRatingDeclarations" && r.ID == ageID:
			s.AgeRating = &r
		}
	}
	slices.SortFunc(s.AppInfoLocs, func(a, b tool.Resource) int { return strings.Compare(a.Str("locale"), b.Str("locale")) })
	return nil
}

func fetchScreenshotSets(ctx context.Context, env *tool.Env, locID string) ([]screenshotSet, error) {
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersionLocalizations/"+locID+"/appScreenshotSets", url.Values{
		"include":                {"appScreenshots"},
		"fields[appScreenshots]": {"assetDeliveryState"},
		"limit[appScreenshots]":  {"10"},
		"limit":                  {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list screenshot sets: %w", err)
	}
	shots := map[string]string{}
	for _, r := range resp.Included {
		if r.Type == "appScreenshots" {
			st, _ := tool.Unwrap(r.Attributes["assetDeliveryState"]).(map[string]any)
			state, _ := st["state"].(string)
			shots[r.ID] = state
		}
	}
	var out []screenshotSet
	for _, set := range resp.Data {
		ss := screenshotSet{Display: applemeta.DisplayType(set.Str("screenshotDisplayType"))}
		for _, id := range set.RelIDs("appScreenshots") {
			ss.States = append(ss.States, shots[id])
		}
		out = append(out, ss)
	}
	return out, nil
}

// exists GETs path and reports false on 404.
func exists(ctx context.Context, env *tool.Env, path string, q url.Values) (bool, error) {
	_, err := env.ASC.Get(ctx, path, q)
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("get %s: %w", path, err)
	}
}

// availabilityID returns the app's appAvailabilities (v2) id, "" when none.
func availabilityID(ctx context.Context, env *tool.Env, appID string) (string, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appAvailabilityV2", nil)
	switch {
	case err == nil:
		if r, ok := resp.First(); ok {
			return r.ID, nil
		}
		return "", nil
	case isNotFound(err):
		return "", nil
	default:
		return "", fmt.Errorf("get app availability: %w", err)
	}
}

// koreaAvailable reports whether the app is available in South Korea (KOR).
func koreaAvailable(ctx context.Context, env *tool.Env, availID string) (*bool, error) {
	res, err := env.ASC.List(ctx, "/v2/appAvailabilities/"+availID+"/territoryAvailabilities", tool.ListOptions{
		Fields:   map[string][]string{"territoryAvailabilities": {"available", "territory"}},
		MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list territory availabilities: %w", err)
	}
	no := false
	for _, t := range res.Data {
		if t.RelID("territory") == "KOR" {
			yes, _ := t.Bool("available")
			return &yes, nil
		}
	}
	return &no, nil
}

// newerVersion reports a > b for dotted numeric version strings.
func newerVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func loadProducts(ctx context.Context, env *tool.Env, s *statusSnapshot) error {
	iaps, err := env.ASC.List(ctx, "/v1/apps/"+s.App.ID+"/inAppPurchasesV2", tool.ListOptions{Include: []string{"versions"}, Query: url.Values{"limit[versions]": {"5"}}})
	if err != nil {
		return fmt.Errorf("list in-app purchases: %w", err)
	}
	versionState := map[string]string{}
	for _, r := range iaps.Included {
		if r.Type == "inAppPurchaseVersions" {
			versionState[r.ID] = r.Str("state")
		}
	}
	for _, p := range iaps.Data {
		name := p.Str("name")
		if name == "" {
			name = p.Str("productId")
		}
		var states []string
		for _, id := range p.RelIDs("versions") {
			states = append(states, versionState[id])
		}
		st := ""
		switch {
		case slices.Contains(states, "READY_FOR_REVIEW"):
			st = "READY_FOR_REVIEW"
		case slices.Contains(states, "PREPARE_FOR_SUBMISSION") && p.Str("state") != "APPROVED":
			st = "PREPARE_FOR_SUBMISSION"
		case slices.Contains(states, "REJECTED"):
			st = "REJECTED"
		case slices.Contains(states, "DEVELOPER_REJECTED"):
			st = "DEVELOPER_REJECTED"
		case len(states) == 0:
			st = p.Str("state")
		}
		if st == "" {
			continue
		}
		s.IAPs[name] = st
		if slices.Contains(iapReady, st) {
			s.ReadyIAPs = append(s.ReadyIAPs, productLabel(p))
		}
	}
	groups, err := env.ASC.List(ctx, "/v1/apps/"+s.App.ID+"/subscriptionGroups", tool.ListOptions{Include: []string{"subscriptions"}, Query: url.Values{"limit[subscriptions]": {"50"}}})
	if err != nil {
		return fmt.Errorf("list subscription groups: %w", err)
	}
	for _, r := range groups.Included {
		if r.Type == "subscriptions" {
			name := r.Str("name")
			if name == "" {
				name = r.Str("productId")
			}
			s.Subscriptions[name] = r.Str("state")
			if slices.Contains(subReady, r.Str("state")) {
				s.ReadySubscriptions = append(s.ReadySubscriptions, productLabel(r))
			}
		}
	}
	return nil
}

// planRelease computes what release_prepare would change and proposes the
// release_prepare (and optionally release_submit) plans.
func planRelease(ctx context.Context, env *tool.Env, s *statusSnapshot, in statusIn) (*statusPlan, []tool.NextStep, error) {
	d := *in.Desired
	plan := &statusPlan{Changes: []string{}}
	versionString := string(s.VersionRef)
	if s.Version != nil {
		versionString = s.Version.Version.Str("versionString")
	} else if !concreteVersion(s.VersionRef) {
		plan.Notes = append(plan.Notes, "no editable version: pass version (e.g. \"1.4\") to plan its creation")
		return plan, nil, nil
	}
	wn, err := normalizeWhatsNew(d.WhatsNew)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
	}
	rt := strings.ToUpper(d.ReleaseType)
	if rt == releaseScheduled && d.EarliestReleaseDate == "" {
		return nil, nil, fmt.Errorf("%w: release_type SCHEDULED needs earliest_release_date", tool.ErrInvalidArgs)
	}

	var cur tool.Resource
	if s.Version == nil {
		plan.Changes = append(plan.Changes, fmt.Sprintf("create %s version %s", s.Platform, versionString))
	} else {
		cur = s.Version.Version
	}

	// Build.
	buildID, curBuildID := "", ""
	var target *tool.Resource
	if s.Version != nil {
		if b, ok := s.build(); ok {
			curBuildID, target = b.ID, &b
		}
	}
	if d.Build != "" {
		b, err := env.Resolve.Build(ctx, s.App.ID, s.Platform, versionedBuildRef(d.Build, versionString))
		if err != nil {
			return nil, nil, err
		}
		buildID = b.ID
		if b.ID != curBuildID {
			plan.Changes = append(plan.Changes, fmt.Sprintf("attach build %s (%s)", b.Number, b.ID))
			target = nil
			if b.ProcessingState != "VALID" {
				plan.Notes = append(plan.Notes, "build "+b.Number+" is "+b.ProcessingState+"; wait with builds_wait before submitting")
			}
		}
	}

	// What's New: existing locales only.
	intendedWN := map[string]string{}
	if len(wn) > 0 {
		existing := map[string]string{}
		if s.Version != nil {
			for _, l := range s.Version.localizations() {
				existing[l.Str("locale")] = l.Str("whatsNew")
			}
		}
		switch {
		case !s.PriorReleased:
			plan.Notes = append(plan.Notes, "first version: What's New is skipped (Apple rejects it on a first release)")
		case s.Version == nil:
			// The new version's locales are copied from the previous one;
			// release_prepare sets What's New on them in the same call.
			for _, loc := range slices.Sorted(maps.Keys(wn)) {
				if err := applemeta.Check(applemeta.FieldWhatsNew, wn[loc]); err != nil {
					return nil, nil, fmt.Errorf("%w: whats_new %s: %v", tool.ErrInvalidArgs, loc, err)
				}
				if loc == "*" {
					plan.Changes = append(plan.Changes, "set What's New for every locale of the new version")
					continue
				}
				plan.Changes = append(plan.Changes, "set What's New for "+loc+" (if the new version has it)")
			}
		default:
			if all, ok := wn["*"]; ok {
				for loc := range existing {
					intendedWN[loc] = all
				}
			}
			for loc, text := range wn {
				if loc == "*" {
					continue
				}
				if _, ok := existing[loc]; !ok {
					plan.SkippedLocales = append(plan.SkippedLocales, loc)
					continue
				}
				intendedWN[loc] = text
			}
			for loc, text := range intendedWN {
				if err := applemeta.Check(applemeta.FieldWhatsNew, text); err != nil {
					return nil, nil, fmt.Errorf("%w: whats_new %s: %v", tool.ErrInvalidArgs, loc, err)
				}
				if existing[loc] == text {
					delete(intendedWN, loc)
				}
			}
			for _, loc := range slices.Sorted(maps.Keys(intendedWN)) {
				plan.Changes = append(plan.Changes, "set What's New for "+loc)
			}
			slices.Sort(plan.SkippedLocales)
			if len(plan.SkippedLocales) > 0 {
				plan.Notes = append(plan.Notes, "skipped_locales do not exist on the version; add them with metadata_plan (they need a description and keywords)")
			}
		}
	}

	// Release type.
	if rt != "" && (rt != cur.Str("releaseType") || d.EarliestReleaseDate != cur.Str("earliestReleaseDate")) {
		plan.Changes = append(plan.Changes, strings.TrimSpace("set release type "+rt+" "+d.EarliestReleaseDate))
	} else if rt == "" && s.Version != nil {
		plan.Notes = append(plan.Notes, "release type stays "+cur.Str("releaseType"))
	}

	// Phased release.
	if d.Phased != nil {
		hasPhased := false
		if s.Version != nil {
			_, hasPhased = s.Version.one("appStoreVersionPhasedRelease", "appStoreVersionPhasedReleases")
		}
		switch {
		case *d.Phased && !hasPhased:
			plan.Changes = append(plan.Changes, "enable phased release (starts INACTIVE, begins on release)")
		case !*d.Phased && hasPhased:
			plan.Notes = append(plan.Notes, "phased release already configured; remove it with release_phased")
		}
	}

	// Export compliance.
	if d.UsesNonExemptEncryption != nil {
		switch {
		case target == nil && buildID == "":
			plan.Notes = append(plan.Notes, "no build to set export compliance on")
		case target == nil:
			plan.Changes = append(plan.Changes, fmt.Sprintf("answer export compliance on the attached build (usesNonExemptEncryption=%t) if unanswered", *d.UsesNonExemptEncryption))
		default:
			if v, set := target.Bool("usesNonExemptEncryption"); !set {
				plan.Changes = append(plan.Changes, fmt.Sprintf("set usesNonExemptEncryption=%t on build %s", *d.UsesNonExemptEncryption, target.Str("version")))
			} else if v != *d.UsesNonExemptEncryption {
				plan.Notes = append(plan.Notes, fmt.Sprintf("build already declares usesNonExemptEncryption=%t (from Info.plist); upload a new build to change it", v))
			}
		}
	}

	subState := ""
	if s.Submission != nil {
		subState = s.Submission.Str("state")
	}
	current := stateFromVersion(versionString, s.Platform, s.Version, subState)
	args := nextArgs(s.App, s.Platform, versionString, d)
	var next []tool.NextStep

	if len(plan.Changes) > 0 {
		key, err := prepareArgsKey(s.App, s.Platform, versionString, buildID, d)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
		}
		fp, err := current.fingerprint()
		if err != nil {
			return nil, nil, err
		}
		id, err := env.Plans.Propose(ctx, "release_prepare", key, fp, tool.PlanPreview{
			Summary:  fmt.Sprintf("prepare %s %s %s", s.App.Name, s.Platform, versionString),
			Current:  current,
			Intended: plan.Changes,
			Affected: []string{s.App.ID},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("propose release_prepare: %w", err)
		}
		plan.PreparePlanID = id
		a := maps.Clone(args)
		a["plan_id"] = id
		next = append(next, tool.NextStep{Tool: "release_prepare", Args: a})
	} else {
		plan.Notes = append(plan.Notes, "nothing to prepare")
	}

	if d.Submit {
		fp, err := intendedState(current, buildID, intendedWN).fingerprint()
		if err != nil {
			return nil, nil, err
		}
		preview := tool.PlanPreview{
			Summary:      fmt.Sprintf("submit %s %s %s for App Review", s.App.Name, s.Platform, versionString),
			Affected:     []string{s.App.ID},
			Irreversible: []string{"App Review starts; the version is locked until review ends or the submission is cancelled"},
		}
		if products := readyProducts(s); len(products) > 0 {
			preview.Intended = append([]string{"submit the version"}, products...)
		}
		id, err := env.Plans.Propose(ctx, "release_submit", submitArgsKey(s.App, s.Platform, versionString), fp, preview)
		if err != nil {
			return nil, nil, fmt.Errorf("propose release_submit: %w", err)
		}
		plan.SubmitPlanID = id
		next = append(next, tool.NextStep{Tool: "release_submit", Args: map[string]any{
			"app": s.App.ID, "platform": string(s.Platform), "version": versionString, "plan_id": id,
		}})
		plan.Notes = append(plan.Notes, "run release_prepare first; release_submit's plan assumes its changes, and fix every block check before submitting")
	}
	return plan, next, nil
}

// nextArgs renders the release_prepare arguments matching desired.
func nextArgs(app tool.App, platform tool.Platform, version string, d releaseDesired) map[string]any {
	a := map[string]any{"app": app.ID, "platform": string(platform), "version": version}
	if d.Build != "" {
		a["build"] = string(d.Build)
	}
	if len(d.WhatsNew) > 0 {
		a["whats_new"] = d.WhatsNew
	}
	if d.ReleaseType != "" {
		a["release_type"] = strings.ToUpper(d.ReleaseType)
	}
	if d.EarliestReleaseDate != "" {
		a["earliest_release_date"] = d.EarliestReleaseDate
	}
	if d.Phased != nil {
		a["phased"] = *d.Phased
	}
	if d.UsesNonExemptEncryption != nil {
		a["uses_non_exempt_encryption"] = *d.UsesNonExemptEncryption
	}
	return a
}
