package release

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/ascrules"
	"github.com/idoo/hamn/internal/metadata"
	"github.com/idoo/hamn/internal/tool"
)

// prepareIn mirrors releaseDesired's argument names (minus submit) so the
// release_prepare Next step of release_status can be called verbatim.
type prepareIn struct {
	App                     tool.AppRef       `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform                tool.Platform     `json:"platform,omitempty"`
	Version                 tool.VersionRef   `json:"version,omitempty" jsonschema:"version to prepare or create, e.g. 1.4; default: editable"`
	Build                   tool.BuildRef     `json:"build,omitempty" jsonschema:"latest, latest-valid, build number or id"`
	WhatsNew                map[string]string `json:"whats_new,omitempty" jsonschema:"What's New per locale; * = all existing"`
	ReleaseType             string            `json:"release_type,omitempty"`
	EarliestReleaseDate     string            `json:"earliest_release_date,omitempty" jsonschema:"RFC 3339, for SCHEDULED"`
	Phased                  *bool             `json:"phased,omitempty" jsonschema:"7-day phased release"`
	UsesNonExemptEncryption *bool             `json:"uses_non_exempt_encryption,omitempty" jsonschema:"export compliance answer"`
	tool.ConfirmPlan
}

func (in prepareIn) desired() releaseDesired {
	return releaseDesired{
		Build: in.Build, WhatsNew: in.WhatsNew, ReleaseType: in.ReleaseType,
		EarliestReleaseDate: in.EarliestReleaseDate, Phased: in.Phased,
		UsesNonExemptEncryption: in.UsesNonExemptEncryption,
	}
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_prepare", Toolset: tool.Release, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByPlan,
		Title:   "Prepare an App Store version",
		Description: "Apply a release_status plan (plan_id from release_status desired): create version, attach build, " +
			"compliance, release type, phasing, What's New. Never waits for processing.",
		Enums: map[string][]any{
			"platform":     {"IOS", "MAC_OS", "TV_OS", "VISION_OS"},
			"release_type": {releaseManual, releaseAfterApproval, releaseScheduled},
		},
	}, releasePrepare))
}

// Step outcomes of release_prepare.
const (
	stepDone            = "done"
	stepAlready         = "already"
	stepSkipped         = "skipped"
	stepNeedsInput      = "needs_input"
	stepStillProcessing = "still_processing"
)

// prepStep is one step of release_prepare.
type prepStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
}

type prepareVersion struct {
	ID      string `json:"id"`
	String  string `json:"string"`
	Created bool   `json:"created,omitempty"`
}

type prepareOut struct {
	App            tool.App              `json:"app"`
	Platform       tool.Platform         `json:"platform"`
	Version        prepareVersion        `json:"version"`
	Build          *tool.Build           `json:"build,omitempty"`
	Steps          []prepStep            `json:"steps"`
	WhatsNew       []metadata.ItemResult `json:"whats_new,omitempty"`
	SkippedLocales []string              `json:"skipped_locales,omitempty"`
}

func releasePrepare(ctx context.Context, env *tool.Env, in prepareIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	d := in.desired()
	if in.PlanID == "" {
		return nil, tool.WithNext(
			fmt.Errorf("%w: release_prepare applies a plan from release_status; nothing was changed. Call release_status with these arguments as desired, review its plan, then call release_prepare again with plan_id set to its prepare plan_id", tool.ErrNeedsConfirm),
			statusNext(app, platform, string(in.Version), desiredArgs(d)),
		)
	}
	rt := strings.ToUpper(d.ReleaseType)
	if rt == releaseScheduled && d.EarliestReleaseDate == "" {
		return nil, fmt.Errorf("%w: release_type SCHEDULED needs earliest_release_date", tool.ErrInvalidArgs)
	}

	snap, err := loadReleaseSnapshot(ctx, env, app.ID, platform, in.Version)
	if err != nil {
		return nil, err
	}
	versionString := string(in.Version)
	if snap.Detail != nil {
		versionString = snap.Detail.Version.Str("versionString")
	} else if !concreteVersion(in.Version) {
		return nil, errNoVersion(app, platform, in.Version)
	}

	var build *tool.Build
	if d.Build != "" {
		b, err := env.Resolve.Build(ctx, app.ID, platform, d.Build)
		if err != nil {
			return nil, err
		}
		if b.Expired {
			return nil, tool.WithNext(fmt.Errorf("%w: build %s is expired", tool.ErrInvalidArgs, b.Number),
				tool.NextStep{Tool: "builds_list", Args: map[string]any{"app": app.ID}})
		}
		build = &b
	}
	buildID := ""
	if build != nil {
		buildID = build.ID
	}
	key, err := prepareArgsKey(app, platform, versionString, buildID, d)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
	}
	fp, err := snap.State.fingerprint()
	if err != nil {
		return nil, err
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, key, fp, tool.PlanPreview{
		Summary:  fmt.Sprintf("prepare %s %s %s", app.Name, platform, versionString),
		Current:  snap.State,
		Intended: key,
		Affected: []string{app.ID},
	})
	if res != nil {
		return res, nil
	}

	p := &preparer{env: env, app: app, platform: platform, snap: snap, d: d, rt: rt, key: key, build: build}
	p.out = prepareOut{App: app, Platform: platform, Version: prepareVersion{ID: snap.versionID(), String: versionString}, Build: build, Steps: []prepStep{}}
	if err := p.run(wctx); err != nil {
		return nil, tool.WithNext(err, statusNext(app, platform, versionString, nil))
	}
	if p.buildPending {
		return tool.StillProcessing(p.out, "build "+build.Number+" is "+build.ProcessingState+"; wait with builds_wait, then plan again with release_status to attach it").
			Then(tool.NextStep{Tool: "builds_wait", Args: map[string]any{"app": app.ID, "build": build.ID}},
				statusNext(app, platform, versionString, desiredArgs(d))), nil
	}
	return tool.OK(p.out).Then(statusNext(app, platform, versionString, nil)), nil
}

// desiredArgs renders release_status's desired object for d.
func desiredArgs(d releaseDesired) map[string]any {
	a := nextArgs(tool.App{}, "", "", d)
	delete(a, "app")
	delete(a, "platform")
	delete(a, "version")
	return a
}

// preparer executes one confirmed release_prepare.
type preparer struct {
	env          *tool.Env
	app          tool.App
	platform     tool.Platform
	snap         *releaseSnapshot
	d            releaseDesired
	rt           string
	key          releasePlanKey
	build        *tool.Build
	created      bool
	buildPending bool
	out          prepareOut
}

func (p *preparer) step(name, status, detail string) {
	p.out.Steps = append(p.out.Steps, prepStep{Step: name, Status: status, Detail: detail})
}

func (p *preparer) run(ctx context.Context) error {
	steps := []func(context.Context) error{p.ensureVersion, p.attachBuild, p.exportCompliance, p.releaseType, p.phased, p.whatsNew}
	for _, s := range steps {
		if err := s(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ensureVersion creates the version when it does not exist.
func (p *preparer) ensureVersion(ctx context.Context) error {
	if p.snap.Detail != nil {
		return nil
	}
	attrs := map[string]any{"platform": string(p.platform), "versionString": p.out.Version.String}
	if p.rt != "" {
		attrs["releaseType"] = p.rt
		if p.rt == releaseScheduled {
			attrs["earliestReleaseDate"] = p.d.EarliestReleaseDate
		}
	}
	resp, err := doWrite(ctx, p.env, http.MethodPost, "/v1/appStoreVersions",
		jsonapiBody("appStoreVersions", "", attrs, map[string]string{"app": "apps:" + p.app.ID}))
	switch {
	case err == nil:
		v, ok := resp.First()
		if !ok {
			return fmt.Errorf("create version: empty response: %w", tool.ErrInternal)
		}
		p.out.Version.ID, p.out.Version.Created, p.created = v.ID, true, true
		p.step("create_version", stepDone, "")
	case ascrules.IsAlready(tool.Request{Method: http.MethodPost, Path: "/v1/appStoreVersions"}, err):
		id, found, rerr := p.findVersion(ctx)
		if rerr != nil {
			return rerr
		}
		if !found {
			return err
		}
		p.out.Version.ID = id
		p.step("create_version", stepAlready, "version already existed")
		// Re-read it so the remaining steps compare against its real state.
		if p.snap.Detail, err = fetchVersionDetail(ctx, p.env, id); err != nil {
			return err
		}
	default:
		return err
	}
	return nil
}

// findVersion reads back a version by string and platform.
func (p *preparer) findVersion(ctx context.Context) (string, bool, error) {
	resp, err := p.env.ASC.Get(ctx, "/v1/apps/"+p.app.ID+"/appStoreVersions", url.Values{
		"filter[versionString]":    {p.out.Version.String},
		"filter[platform]":         {string(p.platform)},
		"fields[appStoreVersions]": {"versionString,appVersionState"},
	})
	if err != nil {
		return "", false, fmt.Errorf("read back version: %w", err)
	}
	v, ok := resp.First()
	return v.ID, ok, nil
}

// attachBuild links the requested build; a build still processing is
// reported, never waited for.
func (p *preparer) attachBuild(ctx context.Context) error {
	if p.build == nil {
		return nil
	}
	if cur, ok := p.snap.attachedBuild(); ok && cur.ID == p.build.ID {
		p.step("attach_build", stepAlready, "build "+p.build.Number+" is attached")
		return nil
	}
	if p.build.ProcessingState != "VALID" {
		p.buildPending = true
		p.step("attach_build", stepStillProcessing, "build "+p.build.Number+" is "+p.build.ProcessingState)
		return nil
	}
	if _, err := doWrite(ctx, p.env, http.MethodPatch, "/v1/appStoreVersions/"+p.out.Version.ID+"/relationships/build", linkageBody("builds", p.build.ID)); err != nil {
		return err
	}
	p.step("attach_build", stepDone, "build "+p.build.Number)
	return nil
}

// exportCompliance answers usesNonExemptEncryption when the build has no
// answer (Info.plist answers cannot be changed). true needs an approved
// encryption declaration, which is linked to the build.
func (p *preparer) exportCompliance(ctx context.Context) error {
	want := p.d.UsesNonExemptEncryption
	if want == nil || p.buildPending {
		if want != nil {
			p.step("export_compliance", stepSkipped, "build is still processing")
		}
		return nil
	}
	b, ok, err := p.targetBuild(ctx)
	if err != nil {
		return err
	}
	if !ok {
		p.step("export_compliance", stepSkipped, "no build attached")
		return nil
	}
	if v, set := b.Bool("usesNonExemptEncryption"); set {
		if v == *want {
			p.step("export_compliance", stepAlready, fmt.Sprintf("usesNonExemptEncryption=%t", v))
		} else {
			p.step("export_compliance", stepSkipped, fmt.Sprintf("build already declares usesNonExemptEncryption=%t (from Info.plist); upload a new build to change it", v))
		}
		return nil
	}
	if !*want {
		if _, err := doWrite(ctx, p.env, http.MethodPatch, "/v1/builds/"+b.ID,
			jsonapiBody("builds", b.ID, map[string]any{"usesNonExemptEncryption": false}, nil)); err != nil {
			return err
		}
		p.step("export_compliance", stepDone, "usesNonExemptEncryption=false")
		return nil
	}
	declID, err := p.approvedDeclaration(ctx)
	if err != nil {
		return err
	}
	if declID == "" {
		p.out.Steps = append(p.out.Steps, prepStep{
			Step: "export_compliance", Status: stepNeedsInput,
			Detail: "the app uses non-exempt encryption but has no approved encryption declaration; upload the documentation in App Store Connect (App Information > App Encryption Documentation), then run release_status again",
			URL:    ascURL(p.app.ID, "distribution/info"),
		})
		return nil
	}
	if _, err := doWrite(ctx, p.env, http.MethodPatch, "/v1/builds/"+b.ID,
		jsonapiBody("builds", b.ID, map[string]any{"usesNonExemptEncryption": true},
			map[string]string{"appEncryptionDeclaration": "appEncryptionDeclarations:" + declID})); err != nil {
		return err
	}
	p.step("export_compliance", stepDone, "usesNonExemptEncryption=true with declaration "+declID)
	return nil
}

// targetBuild returns the build the version has after attachBuild.
func (p *preparer) targetBuild(ctx context.Context) (tool.Resource, bool, error) {
	cur, attached := p.snap.attachedBuild()
	if p.build == nil || (attached && cur.ID == p.build.ID) {
		return cur, attached, nil
	}
	resp, err := p.env.ASC.Get(ctx, "/v1/builds/"+p.build.ID, url.Values{"fields[builds]": {"version,processingState,usesNonExemptEncryption"}})
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("get build %s: %w", p.build.ID, err)
	}
	b, ok := resp.First()
	return b, ok, nil
}

// approvedDeclaration returns the newest APPROVED encryption declaration of
// the app for the platform ("" when none).
func (p *preparer) approvedDeclaration(ctx context.Context) (string, error) {
	list, err := p.env.ASC.List(ctx, "/v1/apps/"+p.app.ID+"/appEncryptionDeclarations", tool.ListOptions{
		Query:  url.Values{"filter[platform]": {string(p.platform)}},
		Fields: map[string][]string{"appEncryptionDeclarations": {"appEncryptionDeclarationState", "createdDate"}},
	})
	if err != nil {
		return "", fmt.Errorf("list encryption declarations: %w", err)
	}
	id, newest := "", ""
	for _, r := range list.Data {
		if r.Str("appEncryptionDeclarationState") == "APPROVED" && r.Str("createdDate") >= newest {
			id, newest = r.ID, r.Str("createdDate")
		}
	}
	return id, nil
}

// releaseType sets releaseType / earliestReleaseDate on an existing version.
func (p *preparer) releaseType(ctx context.Context) error {
	if p.rt == "" || p.created {
		return nil
	}
	cur := p.snap.Detail.Version
	if p.rt == cur.Str("releaseType") && p.d.EarliestReleaseDate == cur.Str("earliestReleaseDate") {
		p.step("release_type", stepAlready, p.rt)
		return nil
	}
	attrs := map[string]any{"releaseType": p.rt}
	if p.rt == releaseScheduled {
		attrs["earliestReleaseDate"] = p.d.EarliestReleaseDate
	}
	if _, err := doWrite(ctx, p.env, http.MethodPatch, "/v1/appStoreVersions/"+p.out.Version.ID,
		jsonapiBody("appStoreVersions", p.out.Version.ID, attrs, nil)); err != nil {
		return err
	}
	p.step("release_type", stepDone, strings.TrimSpace(p.rt+" "+p.d.EarliestReleaseDate))
	return nil
}

// phased opts the version in to a phased release (INACTIVE until release).
func (p *preparer) phased(ctx context.Context) error {
	if p.d.Phased == nil {
		return nil
	}
	has := false
	if !p.created && p.snap.Detail != nil {
		_, has = p.snap.Detail.one("appStoreVersionPhasedRelease", "appStoreVersionPhasedReleases")
	}
	switch {
	case *p.d.Phased && has:
		p.step("phased_release", stepAlready, "phased release configured")
	case *p.d.Phased:
		if _, err := doWrite(ctx, p.env, http.MethodPost, "/v1/appStoreVersionPhasedReleases",
			jsonapiBody("appStoreVersionPhasedReleases", "", map[string]any{"phasedReleaseState": "INACTIVE"},
				map[string]string{"appStoreVersion": "appStoreVersions:" + p.out.Version.ID})); err != nil {
			return err
		}
		p.step("phased_release", stepDone, "INACTIVE; starts when the version is released")
	case has:
		p.step("phased_release", stepSkipped, "phased release already configured; remove it with release_phased")
	}
	return nil
}

// whatsNew sets What's New on the version's existing locales through the
// metadata engine. It is skipped for a version created by this call and on
// an app's first version.
func (p *preparer) whatsNew(ctx context.Context) error {
	wn := p.key.WhatsNew
	if len(wn) == 0 {
		return nil
	}
	if p.created {
		p.step("whats_new", stepSkipped, "version was just created; run release_status again to plan What's New on its locales")
		return nil
	}
	prior, err := priorReleased(ctx, p.env, p.app.ID, p.platform)
	if err != nil {
		return err
	}
	if !prior {
		p.step("whats_new", stepSkipped, "first version: Apple rejects What's New on an app's first release")
		return nil
	}
	remote := &metadata.Listing{
		App:     metadata.AppInfoFields{VersionID: p.out.Version.ID, VersionLocalizationIDs: map[string]string{}},
		Locales: map[string]*metadata.LocaleFields{},
	}
	for _, l := range p.snap.Detail.localizations() {
		text := l.Str("whatsNew")
		remote.App.VersionLocalizationIDs[l.Str("locale")] = l.ID
		remote.Locales[l.Str("locale")] = &metadata.LocaleFields{WhatsNew: &text}
	}
	desired := &metadata.Listing{Locales: map[string]*metadata.LocaleFields{}}
	set := func(loc, text string) { desired.Locales[loc] = &metadata.LocaleFields{WhatsNew: &text} }
	if all, ok := wn["*"]; ok {
		for loc := range remote.Locales {
			set(loc, all)
		}
	}
	for _, loc := range slices.Sorted(maps.Keys(wn)) {
		if loc == "*" {
			continue
		}
		if _, ok := remote.Locales[loc]; !ok {
			p.out.SkippedLocales = append(p.out.SkippedLocales, loc)
			continue
		}
		set(loc, wn[loc])
	}
	plan, err := metadata.Diff(remote, desired, metadata.DiffOptions{})
	if err != nil {
		return fmt.Errorf("%w: whats_new: %v", tool.ErrInvalidArgs, err)
	}
	if len(plan.Errors) > 0 {
		return fmt.Errorf("%w: whats_new: %s %s", tool.ErrInvalidArgs, plan.Errors[0].Locale, plan.Errors[0].Message)
	}
	if len(plan.Changes) == 0 {
		p.step("whats_new", stepAlready, "What's New is up to date")
		return nil
	}
	results, err := metadata.Apply(ctx, p.env.ASC, plan, metadata.Targets{VersionID: p.out.Version.ID}, nil)
	if err != nil {
		return err
	}
	slices.SortFunc(results, func(a, b metadata.ItemResult) int { return strings.Compare(a.Locale, b.Locale) })
	p.out.WhatsNew = results
	failed := 0
	for _, r := range results {
		if r.Outcome == metadata.OutcomeError {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("whats_new: %d of %d locales failed: %s", failed, len(results), firstError(results))
	}
	p.step("whats_new", stepDone, fmt.Sprintf("%d locales", len(results)))
	return nil
}

func firstError(rs []metadata.ItemResult) string {
	for _, r := range rs {
		if r.Error != "" {
			return r.Locale + ": " + r.Error
		}
	}
	return ""
}

// priorReleased reports whether the app has a version of platform that
// reached the store, using release_status's query and state list.
func priorReleased(ctx context.Context, env *tool.Env, appID string, platform tool.Platform) (bool, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID, url.Values{
		"fields[apps]":             {"appStoreVersions"},
		"include":                  {"appStoreVersions"},
		"limit[appStoreVersions]":  {"50"},
		"fields[appStoreVersions]": {"platform,appVersionState"},
	})
	if err != nil {
		return false, fmt.Errorf("get app versions: %w", err)
	}
	for _, v := range resp.Included {
		if v.Type == "appStoreVersions" && v.Str("platform") == string(platform) && slices.Contains(releasedStates, v.Str("appVersionState")) {
			return true, nil
		}
	}
	return false, nil
}
