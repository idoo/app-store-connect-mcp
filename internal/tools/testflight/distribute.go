package testflight

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxDistributeGroups caps the groups one distribute call handles.
const maxDistributeGroups = 20

// submittedExternalStates are external build states in or past beta review;
// a build in one of them is never submitted again.
var submittedExternalStates = []string{
	"WAITING_FOR_BETA_REVIEW", "IN_BETA_REVIEW", "BETA_APPROVED", "BETA_REJECTED",
	"READY_FOR_BETA_TESTING", "IN_BETA_TESTING",
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_distribute", Toolset: tool.TestFlight, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Distribute a TestFlight build",
		Description: "Give a build to beta groups: export compliance, What to Test per locale (* = all), beta review info, " +
			"external beta review. External or public distribution and compliance answers need confirm:true and " +
			"human approval.",
		Enums: map[string][]any{"platform": platformEnum},
	}, distribute))
}

type distributeIn struct {
	App                     tool.AppRef              `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Build                   tool.BuildRef            `json:"build,omitempty" jsonschema:"number, id, latest (default) or latest-valid"`
	Platform                tool.Platform            `json:"platform,omitempty"`
	Groups                  []tool.GroupRef          `json:"groups" jsonschema:"1-20 beta group names or ids"`
	WhatToTest              map[string]string        `json:"what_to_test,omitempty" jsonschema:"What to Test by locale; * = all existing (else primary)"`
	UsesNonExemptEncryption *bool                    `json:"uses_non_exempt_encryption,omitempty" jsonschema:"export compliance if the build has none (ask the user, never guess)"`
	BetaReview              *betaReviewIn            `json:"beta_review,omitempty" jsonschema:"beta review contact, demo account, notes"`
	BetaAppLocalizations    map[string]betaAppInfoIn `json:"beta_app_localizations,omitempty" jsonschema:"beta app info by locale; * = all existing (else primary)"`
	tool.ConfirmFlag
}

type betaReviewIn struct {
	ContactFirstName    string `json:"contact_first_name,omitempty"`
	ContactLastName     string `json:"contact_last_name,omitempty"`
	ContactPhone        string `json:"contact_phone,omitempty"`
	ContactEmail        string `json:"contact_email,omitempty"`
	DemoAccountName     string `json:"demo_account_name,omitempty"`
	DemoAccountPassword string `json:"demo_account_password,omitempty"`
	DemoAccountRequired *bool  `json:"demo_account_required,omitempty"`
	Notes               string `json:"notes,omitempty"`
}

// attrs returns the betaAppReviewDetails attributes that were given.
func (b betaReviewIn) attrs() map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{
		"contactFirstName": b.ContactFirstName, "contactLastName": b.ContactLastName,
		"contactPhone": b.ContactPhone, "contactEmail": b.ContactEmail,
		"demoAccountName": b.DemoAccountName, "demoAccountPassword": b.DemoAccountPassword, "notes": b.Notes,
	} {
		if v != "" {
			m[k] = v
		}
	}
	if b.DemoAccountRequired != nil {
		m["demoAccountRequired"] = *b.DemoAccountRequired
	}
	return m
}

type betaAppInfoIn struct {
	FeedbackEmail    string `json:"feedback_email,omitempty"`
	Description      string `json:"description,omitempty"`
	MarketingURL     string `json:"marketing_url,omitempty"`
	PrivacyPolicyURL string `json:"privacy_policy_url,omitempty"`
}

// attrs returns the betaAppLocalizations attributes that were given.
func (b betaAppInfoIn) attrs() map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{
		"feedbackEmail": b.FeedbackEmail, "description": b.Description,
		"marketingUrl": b.MarketingURL, "privacyPolicyUrl": b.PrivacyPolicyURL,
	} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

func (b betaAppInfoIn) check() error {
	switch {
	case len(b.attrs()) == 0:
		return errors.New("no fields given")
	case b.FeedbackEmail != "" && !validEmail(b.FeedbackEmail):
		return fmt.Errorf("feedback_email %q is not an email address", b.FeedbackEmail)
	case !httpsURL(b.MarketingURL) || !httpsURL(b.PrivacyPolicyURL):
		return errors.New("marketing_url and privacy_policy_url must be http(s) URLs")
	}
	return applemeta.Check(applemeta.FieldBetaDescription, b.Description)
}

func httpsURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// buildState is what distribute needs to know about a build (one GET).
type buildState struct {
	version, processing string
	expired             bool
	encryption          *bool
	groups              []string
	whatToTest          map[string]tool.Resource // locale → betaBuildLocalization
	reviewState         string                   // betaAppReviewSubmission.betaReviewState
	externalState       string                   // buildBetaDetail.externalBuildState
}

func loadBuildState(ctx context.Context, env *tool.Env, buildID string) (*buildState, error) {
	res, err := env.ASC.Get(ctx, "/v1/builds/"+buildID, url.Values{
		"include": {"betaGroups,betaBuildLocalizations,buildBetaDetail,betaAppReviewSubmission"},
		"fields[builds]": {"version,processingState,expired,usesNonExemptEncryption," +
			"betaGroups,betaBuildLocalizations,buildBetaDetail,betaAppReviewSubmission"},
		"fields[betaGroups]":               {"name"},
		"fields[betaBuildLocalizations]":   {"locale,whatsNew"},
		"fields[buildBetaDetails]":         {"internalBuildState,externalBuildState"},
		"fields[betaAppReviewSubmissions]": {"betaReviewState,submittedDate"},
		"limit[betaGroups]":                {"50"},
		"limit[betaBuildLocalizations]":    {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("read build %s: %w", buildID, err)
	}
	b, ok := res.First()
	if !ok {
		return nil, fmt.Errorf("build %s: %w: empty response", buildID, tool.ErrUnavailable)
	}
	inc := includedByID(res.Included)
	st := &buildState{
		version:    b.Str("version"),
		processing: b.Str("processingState"),
		groups:     b.RelIDs("betaGroups"),
		whatToTest: map[string]tool.Resource{},
	}
	st.expired, _ = b.Bool("expired")
	if v, ok := b.Bool("usesNonExemptEncryption"); ok {
		st.encryption = &v
	}
	for _, id := range b.RelIDs("betaBuildLocalizations") {
		if l, ok := inc["betaBuildLocalizations/"+id]; ok {
			st.whatToTest[l.Str("locale")] = l
		}
	}
	if s, ok := inc["betaAppReviewSubmissions/"+b.RelID("betaAppReviewSubmission")]; ok {
		st.reviewState = s.Str("betaReviewState")
	}
	if d, ok := inc["buildBetaDetails/"+b.RelID("buildBetaDetail")]; ok {
		st.externalState = d.Str("externalBuildState")
	}
	return st, nil
}

type groupOutcome struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Internal bool   `json:"internal"`
	Outcome  string `json:"outcome"`
	Error    string `json:"error,omitempty"`
}

type complianceOut struct {
	UsesNonExemptEncryption bool   `json:"uses_non_exempt_encryption"`
	Outcome                 string `json:"outcome"`
}

type reviewDetailOut struct {
	Outcome string   `json:"outcome"`
	Fields  []string `json:"fields,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type betaReviewOut struct {
	State   string `json:"state,omitempty"`
	Outcome string `json:"outcome"` // submitted|already|rejected|skipped|error
	Note    string `json:"note,omitempty"`
}

type distributeOut struct {
	App                  appRef           `json:"app"`
	Platform             tool.Platform    `json:"platform"`
	Build                buildOut         `json:"build"`
	Compliance           *complianceOut   `json:"compliance,omitempty"`
	WhatToTest           []localeOutcome  `json:"what_to_test,omitempty"`
	BetaAppLocalizations []localeOutcome  `json:"beta_app_localizations,omitempty"`
	BetaReviewDetail     *reviewDetailOut `json:"beta_review_detail,omitempty"`
	Groups               []groupOutcome   `json:"groups"`
	BetaReview           *betaReviewOut   `json:"beta_review,omitempty"`
	Errors               int              `json:"errors,omitempty"`
}

// distribution is one distribute call: inputs, the remote state read before
// any write, and the outcome being built.
type distribution struct {
	env      *tool.Env
	app      tool.App
	build    tool.Build
	groups   []tool.Group
	st       *buildState
	out      *distributeOut
	changed  int
	warnings []string
}

func distribute(ctx context.Context, env *tool.Env, in distributeIn) (*tool.Result, error) {
	if len(in.Groups) == 0 || len(in.Groups) > maxDistributeGroups {
		return nil, tool.InvalidArgs("groups: pass 1-%d beta groups", maxDistributeGroups)
	}
	wttAll, wttBy, err := localeMap("what_to_test", in.WhatToTest, func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("empty text")
		}
		return applemeta.Check(applemeta.FieldWhatToTest, s)
	})
	if err != nil {
		return nil, err
	}
	infoAll, infoBy, err := localeMap("beta_app_localizations", in.BetaAppLocalizations, betaAppInfoIn.check)
	if err != nil {
		return nil, err
	}
	var reviewAttrs map[string]any
	if in.BetaReview != nil {
		reviewAttrs = in.BetaReview.attrs()
		if len(reviewAttrs) == 0 {
			return nil, tool.InvalidArgs("beta_review: no fields given")
		}
		if e := in.BetaReview.ContactEmail; e != "" && !validEmail(e) {
			return nil, tool.InvalidArgs("beta_review.contact_email %q is not an email address", e)
		}
		if err := applemeta.Check(applemeta.FieldReviewNotes, in.BetaReview.Notes); err != nil {
			return nil, tool.InvalidArgs("beta_review.notes: %v", err)
		}
	}

	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	bref := in.Build
	if bref == "" {
		bref = tool.BuildLatest
	}
	build, err := env.Resolve.Build(ctx, app.ID, platform, bref)
	if err != nil {
		return nil, err
	}
	d := &distribution{env: env, app: app, build: build}
	seen := map[string]bool{}
	for _, g := range in.Groups {
		grp, err := env.Resolve.Group(ctx, app.ID, g)
		if err != nil {
			return nil, err
		}
		if !seen[grp.ID] {
			seen[grp.ID] = true
			d.groups = append(d.groups, grp)
		}
	}
	if d.st, err = loadBuildState(ctx, env, build.ID); err != nil {
		return nil, err
	}
	d.out = &distributeOut{
		App: appOut(app), Platform: platform,
		Build: buildOut{ID: build.ID, Number: d.st.version, ProcessingState: d.st.processing},
	}
	if d.out.Build.Number == "" {
		d.out.Build.Number = build.Number
	}

	// Gates: nothing is written unless the build can be distributed.
	switch {
	case d.st.expired:
		return nil, tool.WithNext(tool.InvalidArgs("build %s is expired and cannot be distributed", d.out.Build.Number),
			tool.NextStep{Tool: "builds_list", Args: map[string]any{"app": app.ID}})
	case d.st.processing != "VALID":
		return tool.StillProcessing(d.out, "build "+d.out.Build.Number+" is "+d.st.processing+
			"; nothing was changed. Wait with builds_wait, then call testflight_distribute again").
			Then(tool.NextStep{Tool: "builds_wait", Args: map[string]any{"app": app.ID, "build": build.ID}}), nil
	case d.st.encryption == nil && in.UsesNonExemptEncryption == nil:
		return tool.OK(map[string]any{
			"status":      "needs_input",
			"needs_input": "uses_non_exempt_encryption",
			"question": "Does this build use encryption that is not exempt from US export rules? HTTPS/TLS and " +
				"Apple's OS crypto APIs only → false; proprietary or non-standard algorithms → true.",
			"next":  "nothing was changed; ask the user, then call testflight_distribute again with the same arguments plus uses_non_exempt_encryption",
			"app":   d.out.App,
			"build": d.out.Build,
		}), nil
	}

	// Reads the writes depend on, before any write.
	primary := primaryLocale(ctx, env, app.ID)
	wtt, wttLocales, err := localeTargets(wttAll, wttBy, slices.Sorted(maps.Keys(d.st.whatToTest)), primary)
	if err != nil {
		return nil, err
	}
	var infoCur map[string]tool.Resource
	var info map[string]betaAppInfoIn
	var infoLocales []string
	if len(in.BetaAppLocalizations) > 0 {
		if infoCur, err = betaAppLocalizations(ctx, env, app.ID); err != nil {
			return nil, err
		}
		if info, infoLocales, err = localeTargets(infoAll, infoBy, slices.Sorted(maps.Keys(infoCur)), primary); err != nil {
			return nil, err
		}
	}
	external := slices.ContainsFunc(d.groups, func(g tool.Group) bool { return !g.Internal })
	var reviewDetail tool.Resource
	if reviewAttrs != nil {
		if !external {
			d.warnings = append(d.warnings, "beta_review ignored: beta App Review applies to external groups only")
			reviewAttrs = nil
		} else if reviewDetail, err = betaReviewDetail(ctx, env, app.ID); err != nil {
			return nil, err
		}
	}

	var reviewDiff map[string]any
	if reviewAttrs != nil {
		reviewDiff = changedAttrs(reviewDetail, reviewAttrs)
	}
	gctx, gres := d.gate(ctx, in, reviewDiff)
	if gres != nil {
		return gres, nil
	}
	ctx = gctx
	if err := d.compliance(ctx, in.UsesNonExemptEncryption); err != nil {
		return nil, err
	}
	d.whatToTest(ctx, wtt, wttLocales)
	d.betaAppInfo(ctx, info, infoLocales, infoCur)
	if reviewAttrs != nil {
		d.reviewDetail(ctx, reviewDetail, reviewAttrs)
	}
	d.addToGroups(ctx)
	if external {
		d.submitForReview(ctx)
	}

	if d.changed == 0 && d.out.Errors == 0 {
		res := tool.Already(d.out)
		for _, w := range d.warnings {
			res.Warn(w)
		}
		return res, nil
	}
	res := tool.OK(d.out)
	for _, w := range d.warnings {
		res.Warn(w)
	}
	if r := d.out.BetaReview; r != nil && r.Outcome == outcomeRejected {
		res.Warn("Beta App Review rejected this build; calling again will not resubmit it. See beta_review.note").
			Then(tool.NextStep{Tool: "builds_list", Args: map[string]any{"app": app.ID}})
	} else if d.out.Errors > 0 {
		res.Warn(fmt.Sprintf("%d step(s) failed; see error fields. Calling again retries only what is missing", d.out.Errors))
	}
	return res, nil
}

// gate lets additive internal distributions (and repeats that change nothing
// public) through unconfirmed. It demands confirm:true and a human when the
// call would add the build to an external (possibly public-link) group, submit
// it for beta App Review, answer export compliance or overwrite beta review
// details.
func (d *distribution) gate(ctx context.Context, in distributeIn, review map[string]any) (context.Context, *tool.Result) {
	answer := d.st.encryption == nil && in.UsesNonExemptEncryption != nil
	var newExternal []string
	external := false
	for _, g := range d.groups {
		if !g.Internal {
			external = true
			if !slices.Contains(d.st.groups, g.ID) {
				newExternal = append(newExternal, g.Name)
			}
		}
	}
	submit := external && d.st.reviewState == "" && !slices.Contains(submittedExternalStates, d.st.externalState)
	if len(newExternal) == 0 && !submit && !answer && len(review) == 0 {
		return tool.Unconfirmed(ctx, d.env)
	}
	groups := make([]string, 0, len(d.groups))
	for _, g := range d.groups {
		kind := "internal"
		if !g.Internal {
			kind = "external (may have a public link)"
		}
		groups = append(groups, fmt.Sprintf("group %s (%s): %s", g.Name, g.ID, kind))
	}
	intended := map[string]any{"build": d.out.Build, "groups": groups}
	var irreversible []string
	if answer {
		intended["uses_non_exempt_encryption"] = *in.UsesNonExemptEncryption
		irreversible = append(irreversible, "the export compliance answer cannot be changed on this build once set")
	}
	if len(newExternal) > 0 {
		irreversible = append(irreversible, "external testers (and anyone with the group's public link) get the build once Beta App Review approves it")
	}
	if submit {
		intended["beta_review_submission"] = "submit build " + d.out.Build.Number + " to beta App Review"
	}
	if len(review) > 0 {
		intended["beta_review_detail_overwrite"] = review
	}
	if len(in.WhatToTest) > 0 {
		intended["what_to_test"] = in.WhatToTest
	}
	return tool.RequireHumanConfirm(ctx, d.env, in.Confirm, tool.PlanPreview{
		Summary:      fmt.Sprintf("distribute build %s of %s to %d group(s)", d.out.Build.Number, d.app.ID, len(d.groups)),
		Affected:     groups,
		Intended:     intended,
		Irreversible: irreversible,
	})
}

// compliance answers export compliance when the build has no answer.
func (d *distribution) compliance(ctx context.Context, want *bool) error {
	if d.st.encryption != nil {
		d.out.Compliance = &complianceOut{UsesNonExemptEncryption: *d.st.encryption, Outcome: outcomeAlready}
		if want != nil && *want != *d.st.encryption {
			d.warnings = append(d.warnings, fmt.Sprintf(
				"uses_non_exempt_encryption=%t ignored: the build already answers %t (Info.plist or an earlier answer)",
				*want, *d.st.encryption))
		}
		return nil
	}
	_, err := write(ctx, d.env, http.MethodPatch, "/v1/builds/"+d.build.ID,
		patchBody("builds", d.build.ID, map[string]any{"usesNonExemptEncryption": *want}))
	switch {
	case tool.IsConflict(err):
		// Info.plist answered it meanwhile; the next call reads the value.
		d.out.Compliance = &complianceOut{UsesNonExemptEncryption: *want, Outcome: outcomeAlready}
		d.warnings = append(d.warnings, "export compliance was already answered on the build; Apple refused to change it")
	case err != nil:
		return fmt.Errorf("set export compliance on build %s: %w", d.build.ID, err)
	default:
		d.changed++
		d.out.Compliance = &complianceOut{UsesNonExemptEncryption: *want, Outcome: outcomeUpdated}
	}
	if *want {
		d.warnings = append(d.warnings, "uses_non_exempt_encryption=true: attach an approved export compliance "+
			"declaration in App Store Connect before testers can install: https://appstoreconnect.apple.com/apps/"+
			d.app.ID+"/testflight")
	}
	return nil
}

// whatToTest PATCHes existing betaBuildLocalizations and POSTs missing ones.
func (d *distribution) whatToTest(ctx context.Context, want map[string]string, locales []string) {
	for _, loc := range locales {
		o := localeOutcome{Locale: loc}
		text := want[loc]
		var err error
		if cur, ok := d.st.whatToTest[loc]; ok {
			if cur.Str("whatsNew") == text {
				o.Outcome = outcomeUnchanged
			} else {
				_, err = write(ctx, d.env, http.MethodPatch, "/v1/betaBuildLocalizations/"+cur.ID,
					patchBody("betaBuildLocalizations", cur.ID, map[string]any{"whatsNew": text}))
				o.Outcome = outcomeUpdated
			}
		} else {
			_, err = write(ctx, d.env, http.MethodPost, "/v1/betaBuildLocalizations", map[string]any{"data": map[string]any{
				"type":          "betaBuildLocalizations",
				"attributes":    map[string]any{"locale": loc, "whatsNew": text},
				"relationships": map[string]any{"build": toOne("builds", d.build.ID)},
			}})
			o.Outcome = outcomeCreated
		}
		d.record(&o, err)
		d.out.WhatToTest = append(d.out.WhatToTest, o)
	}
}

// betaAppInfo PATCHes changed betaAppLocalizations fields and POSTs missing
// locales.
func (d *distribution) betaAppInfo(ctx context.Context, want map[string]betaAppInfoIn, locales []string, cur map[string]tool.Resource) {
	for _, loc := range locales {
		o := localeOutcome{Locale: loc}
		attrs := want[loc].attrs()
		var err error
		if r, ok := cur[loc]; ok {
			diff := changedAttrs(r, attrs)
			if len(diff) == 0 {
				o.Outcome = outcomeUnchanged
			} else {
				_, err = write(ctx, d.env, http.MethodPatch, "/v1/betaAppLocalizations/"+r.ID,
					patchBody("betaAppLocalizations", r.ID, diff))
				o.Outcome = outcomeUpdated
			}
		} else {
			attrs["locale"] = loc
			_, err = write(ctx, d.env, http.MethodPost, "/v1/betaAppLocalizations", map[string]any{"data": map[string]any{
				"type":          "betaAppLocalizations",
				"attributes":    attrs,
				"relationships": map[string]any{"app": toOne("apps", d.app.ID)},
			}})
			o.Outcome = outcomeCreated
		}
		d.record(&o, err)
		d.out.BetaAppLocalizations = append(d.out.BetaAppLocalizations, o)
	}
}

// record turns a write error into an error outcome and counts changes.
func (d *distribution) record(o *localeOutcome, err error) {
	switch {
	case err != nil:
		o.Outcome, o.Error = outcomeError, errText(err)
		d.out.Errors++
	case o.Outcome != outcomeUnchanged:
		d.changed++
	}
}

// reviewDetail PATCHes the app's beta review detail (a singleton, never
// created) with the fields that differ.
func (d *distribution) reviewDetail(ctx context.Context, cur tool.Resource, want map[string]any) {
	diff := changedAttrs(cur, want)
	if len(diff) == 0 {
		d.out.BetaReviewDetail = &reviewDetailOut{Outcome: outcomeUnchanged}
		return
	}
	out := &reviewDetailOut{Outcome: outcomeUpdated, Fields: slices.Sorted(maps.Keys(diff))}
	if _, err := write(ctx, d.env, http.MethodPatch, "/v1/betaAppReviewDetails/"+cur.ID,
		patchBody("betaAppReviewDetails", cur.ID, diff)); err != nil {
		out.Outcome, out.Error = outcomeError, errText(err)
		d.out.Errors++
	} else {
		d.changed++
	}
	d.out.BetaReviewDetail = out
}

// addToGroups links the build to every group it is not in yet. A 409 is
// reported as already only when a read-back shows the link.
func (d *distribution) addToGroups(ctx context.Context) {
	var conflicts []int
	for _, g := range d.groups {
		o := groupOutcome{ID: g.ID, Name: g.Name, Internal: g.Internal, Outcome: outcomeAlready}
		if !slices.Contains(d.st.groups, g.ID) {
			_, err := write(ctx, d.env, http.MethodPost, "/v1/betaGroups/"+g.ID+"/relationships/builds",
				map[string]any{"data": []map[string]string{ref("builds", d.build.ID)}})
			switch {
			case tool.IsConflict(err):
				o.Outcome, o.Error = outcomeError, errText(err)
				conflicts = append(conflicts, len(d.out.Groups))
			case err != nil:
				o.Outcome, o.Error = outcomeError, errText(err)
			default:
				o.Outcome = outcomeAdded
				d.changed++
			}
		}
		d.out.Groups = append(d.out.Groups, o)
	}
	if len(conflicts) > 0 {
		if st, err := loadBuildState(ctx, d.env, d.build.ID); err == nil {
			for _, i := range conflicts {
				if slices.Contains(st.groups, d.out.Groups[i].ID) {
					d.out.Groups[i].Outcome, d.out.Groups[i].Error = outcomeAlready, ""
				}
			}
		}
	}
	for _, g := range d.out.Groups {
		if g.Outcome == outcomeError {
			d.out.Errors++
		}
	}
}

// submitForReview submits the build to beta App Review unless it already is
// in or past review.
func (d *distribution) submitForReview(ctx context.Context) {
	out := &betaReviewOut{}
	d.out.BetaReview = out
	ok := slices.ContainsFunc(d.out.Groups, func(g groupOutcome) bool { return !g.Internal && g.Outcome != outcomeError })
	switch {
	case d.st.reviewState != "":
		out.State, out.Outcome = d.st.reviewState, outcomeAlready
	case slices.Contains(submittedExternalStates, d.st.externalState):
		out.State, out.Outcome = d.st.externalState, outcomeAlready
	case !ok || d.out.Errors > 0:
		out.Outcome, out.Note = "skipped", "not submitted because an earlier step failed; fix it and call again"
		return
	default:
		res, err := write(ctx, d.env, http.MethodPost, "/v1/betaAppReviewSubmissions", map[string]any{"data": map[string]any{
			"type":          "betaAppReviewSubmissions",
			"relationships": map[string]any{"build": toOne("builds", d.build.ID)},
		}})
		switch {
		case tool.IsConflict(err):
			if s, rerr := d.env.ASC.Get(ctx, "/v1/builds/"+d.build.ID+"/betaAppReviewSubmission",
				url.Values{"fields[betaAppReviewSubmissions]": {"betaReviewState"}}); rerr == nil {
				if r, found := s.First(); found {
					out.State, out.Outcome = r.Str("betaReviewState"), outcomeAlready
					break
				}
			}
			out.Outcome, out.Note = outcomeError, errText(err)
			d.out.Errors++
		case err != nil:
			out.Outcome, out.Note = outcomeError, errText(err)
			d.out.Errors++
		default:
			d.changed++
			out.Outcome = "submitted"
			if r, found := res.First(); found {
				out.State = r.Str("betaReviewState")
			}
		}
	}
	switch out.State {
	case "REJECTED", "BETA_REJECTED":
		// Not "already": external testers never get a rejected build.
		out.Outcome = outcomeRejected
		out.Note = "Beta App Review rejected this build, so external testers will not get it. Read the reason in " +
			"App Store Connect (TestFlight → the build), fix it (beta_review updates the review contact and notes), " +
			"upload a new build, wait for it with builds_wait and distribute that build"
		d.out.Errors++
	case "WAITING_FOR_REVIEW", "IN_REVIEW", "WAITING_FOR_BETA_REVIEW", "IN_BETA_REVIEW":
		out.Note = "external testers get the build once Beta App Review approves it"
	}
}

// betaAppLocalizations maps the app's beta app localizations by locale.
func betaAppLocalizations(ctx context.Context, env *tool.Env, appID string) (map[string]tool.Resource, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/betaAppLocalizations", tool.ListOptions{
		Fields: map[string][]string{"betaAppLocalizations": {
			"locale", "feedbackEmail", "description", "marketingUrl", "privacyPolicyUrl",
		}},
		Limit:    200,
		MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list beta app localizations of app %s: %w", appID, err)
	}
	m := make(map[string]tool.Resource, len(res.Data))
	for _, r := range res.Data {
		m[r.Str("locale")] = r
	}
	return m, nil
}

// betaReviewDetail reads the app's beta review detail singleton.
func betaReviewDetail(ctx context.Context, env *tool.Env, appID string) (tool.Resource, error) {
	res, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/betaAppReviewDetail", url.Values{
		"fields[betaAppReviewDetails]": {"contactFirstName,contactLastName,contactPhone,contactEmail," +
			"demoAccountName,demoAccountPassword,demoAccountRequired,notes"},
	})
	if err != nil {
		return tool.Resource{}, fmt.Errorf("read beta review detail of app %s: %w", appID, err)
	}
	r, ok := res.First()
	if !ok {
		return tool.Resource{}, fmt.Errorf("app %s has no beta review detail: %w", appID, tool.ErrUnavailable)
	}
	return r, nil
}
