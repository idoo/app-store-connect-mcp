// Package library implements the App Store Asset Library write tools:
// library_plan diffs a local directory against the placements of a version,
// custom product page, experiment treatment or in-app event, and
// library_apply carries the plan out (upload, place, delete, reorder,
// retire).
package library

import (
	"context"
	"crypto/md5" //nolint:gosec // content identity, not security
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tools/assetlib"
)

// Targets.
const (
	targetVersion   = "version"
	targetCPP       = "cpp"
	targetTreatment = "experiment_treatment"
	targetEvent     = "event"
)

// Placement types.
const (
	typeScreenshot = "APP_SCREENSHOT"
	typePreview    = "APP_PREVIEW"
	typeHeader     = "PRODUCT_PAGE_HEADER_ASSET"
	typeSearch     = "APP_STORE_SEARCH_RESULTS_ASSET"
	typeEventCard  = "EVENT_CARD_ASSET"
	typeEventPage  = "EVENT_DETAILS_PAGE_ASSET"
)

// Asset categories.
const (
	catScreens  = "APP_SCREENSHOTS_AND_PREVIEWS"
	catCreative = "CREATIVE_ASSETS"
)

const (
	modeAdd     = "add"
	modeReplace = "replace"
	// stateKind is the StateStore kind of the MD5 → asset id index.
	stateKind = "library"
	// maxFiles caps one plan's local files.
	maxFiles = 500
)

var (
	targetEnum = []any{targetVersion, targetCPP, targetTreatment, targetEvent}
	modeEnum   = []any{modeAdd, modeReplace}
)

// creativePrefixes maps a locale-root file stem to its placement types; a
// header-search.* file is one asset placed twice.
var creativePrefixes = map[string][]string{
	"header":        {typeHeader},
	"search":        {typeSearch},
	"header-search": {typeHeader, typeSearch},
	"event-card":    {typeEventCard},
	"event-details": {typeEventPage},
}

type planIn struct {
	App      string        `json:"app,omitempty" jsonschema:"app id, bundle id or name"`
	Target   string        `json:"target,omitempty" jsonschema:"what to place on, default version"`
	ID       string        `json:"id,omitempty" jsonschema:"version: version string (default editable); else the cpp version, treatment or event id"`
	Platform tool.Platform `json:"platform,omitempty"`
	Dir      string        `json:"dir,omitempty" jsonschema:"<locale>/<display>/NN-*; locale root: header.*, search.*, header-search.*, event-card.*, event-details.*"`
	Mode     string        `json:"mode,omitempty" jsonschema:"add (default) or replace placements of the groups in dir"`
	Remove   []string      `json:"remove,omitempty" jsonschema:"placement ids to delete"`
	Archive  []string      `json:"archive,omitempty" jsonschema:"asset ids to retire (placements deleted first)"`
}

type applyIn struct {
	tool.ConfirmPlan
	planIn
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "library_plan", Toolset: tool.Library, Class: tool.Read, Idempotent: true,
		Title: "Plan Asset Library changes",
		Description: "Diffs a screenshots/creatives dir against Asset Library placements of a version, custom product page, " +
			"experiment treatment or in-app event. Validates against Apple's specs and limits; returns a plan_id for library_apply.",
		Enums: map[string][]any{"target": targetEnum, "mode": modeEnum, "platform": {"IOS", "MAC_OS", "TV_OS", "VISION_OS"}},
	}, libraryPlan))
	tool.Register(tool.New(tool.Meta{
		Name: "library_apply", Toolset: tool.Library, Class: tool.Destructive, Confirm: tool.ConfirmByPlan,
		Title: "Apply Asset Library plan",
		Description: "Applies a library_plan: same arguments plus its plan_id. Uploads, creates placements, deletes, reorders, " +
			"retires assets. Refused if placements changed since planning; remove/archive/replace need human approval.",
		Enums: map[string][]any{"target": targetEnum, "mode": modeEnum, "platform": {"IOS", "MAC_OS", "TV_OS", "VISION_OS"}},
	}, libraryApply))
}

// parent is the resolved placement parent.
type parent struct {
	Kind     string        `json:"kind"`
	ID       string        `json:"id"`
	State    string        `json:"state,omitempty"`
	Editable bool          `json:"editable"`
	Platform tool.Platform `json:"platform"`
	locPath  string
	locType  string
	locRel   string
	feature  string
}

// ordering reports whether Apple takes ordering requests for this parent.
func (p parent) ordering() bool { return p.Kind != targetEvent }

var editableStates = []string{
	"PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "METADATA_REJECTED", "INVALID_BINARY", "DRAFT",
	"READY_FOR_REVIEW",
}

func resolveParent(ctx context.Context, env *tool.Env, appID string, in planIn) (parent, error) {
	kind := in.Target
	if kind == "" {
		kind = targetVersion
	}
	switch kind {
	case targetVersion:
		ref := tool.VersionRef(in.ID)
		if ref == "" {
			ref = tool.VersionEditable
		}
		v, err := env.Resolve.Version(ctx, appID, in.Platform, ref)
		if err != nil {
			return parent{}, err
		}
		return parent{
			Kind: kind, ID: v.ID, State: v.State, Editable: slices.Contains(editableStates, v.State), Platform: v.Platform,
			locPath: "/v1/appStoreVersions/" + v.ID + "/appStoreVersionLocalizations",
			locType: "appStoreVersionLocalizations", locRel: "appStoreVersionLocalization", feature: "APP_STORE_VERSIONS",
		}, nil
	case targetCPP, targetTreatment, targetEvent:
		if in.ID == "" {
			return parent{}, tool.InvalidArgs("id is required for target %s", kind)
		}
	default:
		return parent{}, tool.InvalidArgs("unknown target %q", kind)
	}
	var p parent
	var getPath, stateKey string
	switch kind {
	case targetCPP:
		getPath, stateKey = "/v1/appCustomProductPageVersions/"+in.ID, "state"
		p = parent{
			locPath: getPath + "/appCustomProductPageLocalizations", locType: "appCustomProductPageLocalizations",
			locRel: "appCustomProductPageLocalization", feature: "CUSTOM_PRODUCT_PAGES",
		}
	case targetTreatment:
		getPath = "/v1/appStoreVersionExperimentTreatments/" + in.ID
		p = parent{
			locPath: getPath + "/appStoreVersionExperimentTreatmentLocalizations", locType: "appStoreVersionExperimentTreatmentLocalizations",
			locRel: "appStoreVersionExperimentTreatmentLocalization", feature: "PRODUCT_PAGE_OPTIMIZATIONS",
		}
	case targetEvent:
		getPath, stateKey = "/v1/appEvents/"+in.ID, "eventState"
		p = parent{
			locPath: getPath + "/localizations", locType: "appEventLocalizations",
			locRel: "appEventLocalization", feature: "IN_APP_EVENTS",
		}
	}
	resp, err := env.ASC.Get(ctx, getPath, nil)
	if err != nil {
		return parent{}, fmt.Errorf("read %s %s: %w", kind, in.ID, err)
	}
	r, ok := resp.First()
	if !ok {
		return parent{}, &tool.ErrNotFound{Kind: kind, Ref: in.ID}
	}
	p.Kind, p.ID = kind, r.ID
	if stateKey == "" {
		p.Editable = true // treatments carry no state; Apple refuses writes to a running experiment
	} else {
		p.State = r.Str(stateKey)
		p.Editable = slices.Contains(editableStates, p.State)
	}
	return p, nil
}

// localFile is one file of the plan directory.
type localFile struct {
	Rel    string   `json:"file"`
	Locale string   `json:"locale"`
	Types  []string `json:"types"`
	MD5    string   `json:"md5"`
	path   string
	info   assetlib.FileInfo
	dir    string // display dir name ("" at the locale root)
}

func scanDir(env *tool.Env, dir string) ([]localFile, []problem, error) {
	root, err := env.Files.Resolve(dir)
	if err != nil {
		return nil, nil, tool.InvalidArgs("dir: %w", err)
	}
	var files []localFile
	var probs []problem
	err = env.Files.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		f := localFile{Rel: filepath.ToSlash(rel), path: p}
		switch len(parts) {
		case 2:
			stem := strings.TrimSuffix(parts[1], path.Ext(parts[1]))
			types, ok := creativePrefixes[strings.ToLower(stem)]
			if !ok {
				probs = append(probs, problem{File: f.Rel, Kind: "unknown_file", Message: "locale-root files must be header.*, search.*, header-search.*, event-card.* or event-details.*"})
				return nil
			}
			f.Locale, f.Types = parts[0], types
		case 3:
			f.Locale, f.dir = parts[0], parts[1]
		default:
			probs = append(probs, problem{File: f.Rel, Kind: "layout", Message: "expected <locale>/<display>/NN-* or <locale>/<creative>.*"})
			return nil
		}
		if len(files) >= maxFiles {
			return tool.InvalidArgs("dir has more than %d files", maxFiles)
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(files, func(a, b localFile) int { return strings.Compare(a.Rel, b.Rel) })
	for i := range files {
		f := &files[i]
		if err := inspect(env, f); err != nil {
			probs = append(probs, problem{File: f.Rel, Kind: "unreadable", Message: err.Error()})
			f.Types = nil
			continue
		}
		if f.Types == nil {
			f.Types = []string{typeScreenshot}
			if f.info.Video {
				f.Types = []string{typePreview}
			}
		}
	}
	return files, probs, nil
}

func inspect(env *tool.Env, f *localFile) error {
	fh, err := env.Files.Open(f.path)
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	f.info, err = assetlib.Inspect(f.Rel, fh, st.Size())
	if err != nil {
		return err
	}
	h := md5.New() //nolint:gosec // content identity
	if _, err := io.Copy(h, io.NewSectionReader(fh, 0, st.Size())); err != nil {
		return err
	}
	f.MD5 = hex.EncodeToString(h.Sum(nil))
	return nil
}

// problem is a plan finding that blocks apply.
type problem struct {
	File    string `json:"file,omitempty"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type uploadOut struct {
	File     string `json:"file"`
	MD5      string `json:"md5"`
	Kind     string `json:"kind"`
	Category string `json:"category"`
	path     string
	info     assetlib.FileInfo
}

type reuseOut struct {
	File    string `json:"file"`
	AssetID string `json:"asset_id"`
	Why     string `json:"why"`
}

// placementOut is one placement to create; Asset is an asset id or
// "new:<md5>" for an upload of this plan.
type placementOut struct {
	Locale string `json:"locale"`
	Type   string `json:"type"`
	Group  string `json:"group"`
	Asset  string `json:"asset"`
	File   string `json:"file"`
	locID  string
	kind   string
}

type deleteOut struct {
	ID     string `json:"id"`
	Locale string `json:"locale,omitempty"`
	Group  string `json:"group,omitempty"`
	Why    string `json:"why"`
	typ    string
}

// groupInfo is one locale's placement group as planned: its placement
// count when read and Apple's maximum (0 = unknown).
type groupInfo struct{ have, limit int }

// gkey names a placement group of one locale.
func gkey(locale, typ, group string) string { return locale + "\x00" + typ + "\x00" + group }

// orderOut is one ordering request; ids may hold "new:<md5>" placeholders
// resolved after the placements are created.
type orderOut struct {
	Locale string   `json:"loc"`
	Group  string   `json:"group"`
	IDs    []string `json:"ids"`
	locID  string
}

type archiveOut struct {
	AssetID string `json:"asset_id"`
	Kind    string `json:"kind"`
	State   string `json:"state,omitempty"`
	Action  string `json:"action"` // delete or archive
}

type alreadyOut struct {
	Locale string `json:"locale"`
	Group  string `json:"group"`
	ID     string `json:"placement_id"`
	File   string `json:"file"`
}

type planOut struct {
	Status     string         `json:"status"`
	App        string         `json:"app"`
	Target     parent         `json:"target"`
	Mode       string         `json:"mode"`
	Uploads    []uploadOut    `json:"uploads"`
	Reuse      []reuseOut     `json:"reuse"`
	Already    []alreadyOut   `json:"already"`
	Create     []placementOut `json:"placements_create"`
	Delete     []deleteOut    `json:"placements_delete"`
	Order      []orderOut     `json:"order"`
	Archive    []archiveOut   `json:"archive"`
	Problems   []problem      `json:"problems"`
	PlanID     string         `json:"plan_id,omitempty"`
	libraryID  string
	fp         any
	key        planKey
	human      bool
	locByID    map[string]string
	assetKinds map[string]string
	groups     map[string]groupInfo
}

// planKey is the canonical argument set a library_apply plan is bound to.
type planKey struct {
	App     string   `json:"app"`
	Target  string   `json:"target"`
	Parent  string   `json:"parent"`
	Dir     string   `json:"dir"`
	Mode    string   `json:"mode"`
	Remove  []string `json:"remove"`
	Archive []string `json:"archive"`
	Local   string   `json:"local"`
}

// existing is one placement read from Apple.
type existing struct {
	ID, Type, Group, Asset, State string
	Pos                           int
}

func readPlacements(ctx context.Context, env *tool.Env, locType, locID string) ([]existing, error) {
	res, err := env.ASC.List(ctx, "/v1/"+locType+"/"+locID+"/placements", tool.ListOptions{
		Query: url.Values{"sort": {"placementGroupPosition"}}, Limit: 200, MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("read placements of %s: %w", locID, err)
	}
	out := make([]existing, 0, len(res.Data))
	for _, r := range res.Data {
		pos, _ := r.Int("placementGroupPosition")
		a := r.RelID("image")
		if a == "" {
			a = r.RelID("video")
		}
		out = append(out, existing{ID: r.ID, Type: r.Str("placementType"), Group: r.Str("placementGroup"), Asset: a, State: r.Str("state"), Pos: pos})
	}
	return out, nil
}

// stateKey names the MD5 index record; the store allows no "/" in keys.
func stateKey(env *tool.Env, appID string) string { return env.Profile.Name + "__" + appID }

func computePlan(ctx context.Context, env *tool.Env, in planIn) (*planOut, error) {
	if in.Dir == "" && len(in.Remove) == 0 && len(in.Archive) == 0 {
		return nil, tool.InvalidArgs("pass dir, remove or archive")
	}
	mode := in.Mode
	if mode == "" {
		mode = modeAdd
	}
	if mode != modeAdd && mode != modeReplace {
		return nil, tool.InvalidArgs("mode must be add or replace")
	}
	app, err := env.Resolve.App(ctx, tool.AppRef(in.App))
	if err != nil {
		return nil, err
	}
	par, err := resolveParent(ctx, env, app.ID, in)
	if err != nil {
		return nil, err
	}
	if par.Platform == "" {
		par.Platform = app.Platform(in.Platform)
	}
	out := &planOut{
		Status: "ready", App: app.ID, Target: par, Mode: mode,
		Uploads: []uploadOut{}, Reuse: []reuseOut{}, Already: []alreadyOut{}, Create: []placementOut{},
		Delete: []deleteOut{}, Order: []orderOut{}, Archive: []archiveOut{}, Problems: []problem{},
		locByID: map[string]string{}, assetKinds: map[string]string{}, groups: map[string]groupInfo{},
	}
	if !par.Editable {
		out.Problems = append(out.Problems, problem{Kind: "parent_not_editable", Message: fmt.Sprintf("%s %s is in state %s; placements need an editable parent", par.Kind, par.ID, par.State)})
	}
	var files []localFile
	var digest []string
	if in.Dir != "" {
		var probs []problem
		files, probs, err = scanDir(env, in.Dir)
		if err != nil {
			return nil, err
		}
		out.Problems = append(out.Problems, probs...)
		for _, f := range files {
			digest = append(digest, f.Rel+"\x00"+f.MD5)
		}
	}
	ref, err := assetlib.LoadRefData(ctx, env)
	if err != nil {
		return nil, err
	}
	libID, assets, err := assetlib.Assets(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	out.libraryID = libID
	byID := map[string]assetlib.Asset{}
	for _, a := range assets {
		byID[a.ID] = a
		out.assetKinds[a.ID] = a.Kind
	}
	locs, err := env.ASC.List(ctx, par.locPath, tool.ListOptions{Fields: map[string][]string{par.locType: {"locale"}}, MaxItems: 200})
	if err != nil {
		return nil, fmt.Errorf("read localizations: %w", err)
	}
	locByLocale := map[string]string{}
	for _, l := range locs.Data {
		locByLocale[l.Str("locale")] = l.ID
		out.locByID[l.ID] = l.Str("locale")
	}
	current := map[string][]existing{} // locID → placements
	locIDs := make([]string, 0, len(locs.Data))
	for _, l := range locs.Data {
		locIDs = append(locIDs, l.ID)
	}
	slices.Sort(locIDs)
	for _, id := range locIDs {
		ps, err := readPlacements(ctx, env, par.locType, id)
		if err != nil {
			return nil, err
		}
		current[id] = ps
	}

	// Reuse index: MD5 → asset id, verified against the live library.
	var index map[string]string
	if _, err := env.State.Load(stateKind, stateKey(env, app.ID), &index); err != nil {
		return nil, err
	}
	resolved := map[string]string{} // md5 → asset id or new:md5
	reusedStates := map[string]string{}
	assetFor := func(f localFile) string {
		if id, ok := resolved[f.MD5]; ok {
			return id
		}
		var id, why string
		if a, ok := byID[index[f.MD5]]; ok && usableOrPending(a.State) {
			id, why = a.ID, "md5"
		} else {
			for _, a := range assets {
				if usableOrPending(a.State) && a.FileName == f.info.Name && a.FileSize == f.info.Size &&
					(f.info.Video || (a.Width == f.info.Width && a.Height == f.info.Height)) {
					id, why = a.ID, "name_size_dims"
					break
				}
			}
		}
		if id != "" {
			out.Reuse = append(out.Reuse, reuseOut{File: f.Rel, AssetID: id, Why: why})
			reusedStates[id] = byID[id].State
		} else {
			id = "new:" + f.MD5
			kind, cat := "image", catScreens
			if f.info.Video {
				kind = "video"
			}
			if !slices.Contains(f.Types, typeScreenshot) && !slices.Contains(f.Types, typePreview) {
				cat = catCreative
			}
			out.Uploads = append(out.Uploads, uploadOut{File: f.Rel, MD5: f.MD5, Kind: kind, Category: cat, path: f.path, info: f.info})
			out.assetKinds[id] = kind
		}
		resolved[f.MD5] = id
		return id
	}

	type slot struct{ locID, locale, typ, group string }
	desired := map[slot][]placementOut{}
	var slots []slot
	for _, f := range files {
		if f.Types == nil {
			continue
		}
		locID, ok := locByLocale[f.Locale]
		if !ok {
			out.Problems = append(out.Problems, problem{File: f.Rel, Kind: "no_localization", Message: fmt.Sprintf("%s %s has no %s localization", par.Kind, par.ID, f.Locale)})
			continue
		}
		event := f.Types[0] == typeEventCard || f.Types[0] == typeEventPage
		if event != (par.Kind == targetEvent) {
			out.Problems = append(out.Problems, problem{File: f.Rel, Kind: "wrong_target", Message: "event-card/event-details files go only to an event target, and events take only those"})
			continue
		}
		for _, typ := range f.Types {
			group, kind, msg := pickGroup(ref, par.Platform, typ, f)
			if msg != "" {
				out.Problems = append(out.Problems, problem{File: f.Rel, Kind: kind, Message: msg})
				continue
			}
			if _, probs := ref.Check(typ, group, f.info); len(probs) > 0 {
				out.Problems = append(out.Problems, problem{File: f.Rel, Kind: "spec", Message: strings.Join(probs, "; ")})
				continue
			}
			s := slot{locID, f.Locale, typ, group}
			if _, ok := desired[s]; !ok {
				slots = append(slots, s)
			}
			desired[s] = append(desired[s], placementOut{Locale: f.Locale, Type: typ, Group: group, Asset: assetFor(f), File: f.Rel, locID: locID, kind: out.assetKinds[assetFor(f)]})
		}
	}

	removed := map[string]bool{}
	for _, id := range in.Remove {
		found := false
		for locID, ps := range current {
			for _, p := range ps {
				if p.ID == id {
					found = true
					removed[id] = true
					out.Delete = append(out.Delete, deleteOut{ID: id, Locale: out.locByID[locID], Group: p.Group, Why: "remove", typ: p.Type})
				}
			}
		}
		if !found {
			out.Problems = append(out.Problems, problem{Kind: "unknown_placement", Message: fmt.Sprintf("placement %s is not on %s %s", id, par.Kind, par.ID)})
		}
	}
	for _, s := range slots {
		var have []existing
		for _, p := range current[s.locID] {
			if p.Type == s.typ && p.Group == s.group {
				have = append(have, p)
			}
		}
		limit := ref.MaxCount(par.feature, s.typ, s.group)
		out.groups[gkey(s.locale, s.typ, s.group)] = groupInfo{have: len(have), limit: limit}
		keep := map[string]string{} // asset → placement id
		for _, p := range have {
			if !removed[p.ID] {
				keep[p.Asset] = p.ID
			}
		}
		var final []string
		created := false
		wanted := map[string]bool{}
		for _, d := range desired[s] {
			wanted[d.Asset] = true
			if id, ok := keep[d.Asset]; ok {
				out.Already = append(out.Already, alreadyOut{Locale: s.locale, Group: s.group, ID: id, File: d.File})
				final = append(final, id)
				continue
			}
			out.Create = append(out.Create, d)
			final = append(final, d.Asset)
			created = true
		}
		deleted := false
		var kept []string
		for _, p := range have {
			if removed[p.ID] || wanted[p.Asset] {
				deleted = deleted || removed[p.ID]
				continue
			}
			if mode == modeReplace {
				out.Delete = append(out.Delete, deleteOut{ID: p.ID, Locale: s.locale, Group: s.group, Why: "replace", typ: s.typ})
				deleted = true
				continue
			}
			kept = append(kept, p.ID)
		}
		final = append(kept, final...)
		if limit > 0 && len(final) > limit {
			out.Problems = append(out.Problems, problem{Kind: "limit", Message: fmt.Sprintf("%s %s %s: %d placements, Apple allows %d", s.locale, s.typ, s.group, len(final), limit)})
		}
		var cur []string
		for _, p := range have {
			if !removed[p.ID] && (mode != modeReplace || wanted[p.Asset]) {
				cur = append(cur, p.ID)
			}
		}
		if par.ordering() && len(final) > 1 && (created || deleted || !slices.Equal(cur, final)) {
			out.Order = append(out.Order, orderOut{Locale: s.locale, Group: s.group, IDs: final, locID: s.locID})
		}
	}
	for _, id := range in.Archive {
		a, ok := byID[id]
		if !ok {
			out.Problems = append(out.Problems, problem{Kind: "unknown_asset", Message: fmt.Sprintf("asset %s is not in the app's library", id)})
			continue
		}
		action := "delete"
		if retiredByArchive(a.State) {
			action = "archive"
		}
		out.Archive = append(out.Archive, archiveOut{AssetID: id, Kind: a.Kind, State: a.State, Action: action})
	}

	sum := sha256.Sum256([]byte(strings.Join(digest, "\n")))
	out.key = planKey{
		App: app.ID, Target: par.Kind, Parent: par.ID, Dir: in.Dir, Mode: mode,
		Remove: sortedCopy(in.Remove), Archive: sortedCopy(in.Archive), Local: hex.EncodeToString(sum[:]),
	}
	archived := map[string]string{}
	for _, a := range out.Archive {
		archived[a.AssetID] = a.State
	}
	out.fp = map[string]any{"placements": current, "reused": reusedStates, "archive": archived, "parent": par.State}
	out.human = len(in.Remove) > 0 || len(in.Archive) > 0 || (mode == modeReplace && len(out.Delete) > 0)
	switch {
	case len(out.Problems) > 0:
		out.Status = "invalid"
	case len(out.Uploads)+len(out.Create)+len(out.Delete)+len(out.Order)+len(out.Archive) == 0:
		out.Status = "no_changes"
	}
	return out, nil
}

// pickGroup chooses the placement group of f among the accepting groups of
// the target platform. A display directory that names a group or display
// type must match one of them (else a display_mismatch problem: never
// another group that merely fits the pixels); any other directory name falls
// back to the only accepting group. It returns the group, or a problem kind
// and message.
func pickGroup(ref *assetlib.RefData, platform tool.Platform, typ string, f localFile) (group, kind, msg string) {
	stores := storesFor(platform, typ)
	var groups []string
	for _, g := range ref.Groups(typ, f.info) {
		if slices.Contains(stores, ref.GroupPlatform(g)) {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return "", "no_group", fmt.Sprintf("%s %dx%d fits no %s group for %s", f.Rel, f.info.Width, f.info.Height, typ, platform)
	}
	if named, ok := dirGroups(ref, f.dir); ok {
		var match []string
		for _, g := range groups {
			if slices.Contains(named, g) {
				match = append(match, g)
			}
		}
		switch {
		case len(match) == 1:
			return match[0], "", ""
		case len(match) > 1:
			return "", "no_group", fmt.Sprintf("%dx%d fits several %s groups (%s); name the display directory after one", f.info.Width, f.info.Height, typ, strings.Join(match, ", "))
		}
		return "", "display_mismatch", fmt.Sprintf("folder %s names %s, but %s %dx%d fits only %s for %s; move it to a matching folder or fix its size", f.dir, strings.Join(named, "/"), f.Rel, f.info.Width, f.info.Height, strings.Join(groups, ", "), platform)
	}
	if len(groups) == 1 {
		return groups[0], "", ""
	}
	return "", "no_group", fmt.Sprintf("%dx%d fits several %s groups (%s); name the display directory after one", f.info.Width, f.info.Height, typ, strings.Join(groups, ", "))
}

// dirGroups returns the placement groups a display directory name names: a
// group (with or without _PROFILE) or a display type (enum, including the
// pending APP_IPHONE_DUO, or a friendly name). ok is false for a name that
// names no display, which keeps the pixel-size fallback.
func dirGroups(ref *assetlib.RefData, dir string) ([]string, bool) {
	if dir == "" {
		return nil, false
	}
	up := strings.ToUpper(dir)
	for _, g := range []string{up, up + "_PROFILE"} {
		if ref.GroupPlatform(g) != "" {
			return []string{g}, true
		}
	}
	d := applemeta.DisplayType(up)
	if pd, err := applemeta.ParseDisplay(dir); err == nil {
		d = pd
	}
	if gs := assetlib.GroupsForDisplay(d); len(gs) > 0 {
		return gs, true
	}
	return nil, false
}

// storesFor lists the refData group platforms a target platform accepts.
// ANY (DEFAULT_PROFILE creatives) always fits; iMessage groups only take
// IMESSAGE_APP_SCREENSHOT; watch groups belong to iOS versions.
func storesFor(platform tool.Platform, typ string) []string {
	out := []string{"ANY"}
	switch platform {
	case tool.IOS:
		out = append(out, "IPHONE_APP_STORE", "IPAD_APP_STORE", "WATCH_APP_STORE")
		if typ == "IMESSAGE_APP_SCREENSHOT" {
			out = append(out, "IMESSAGE_APP_STORE")
		}
	case tool.MacOS:
		out = append(out, "MAC_APP_STORE")
	case tool.TVOS:
		out = append(out, "TV_APP_STORE")
	case tool.VisionOS:
		out = append(out, "VISION_PRO_APP_STORE")
	}
	return out
}

// usableOrPending reports an asset that may be reused: usable or still
// processing a successful upload.
func usableOrPending(state string) bool {
	return assetlib.Usable(state) || state == "UPLOAD_COMPLETE"
}

// retiredByArchive reports assets Apple keeps (reviewed), which are archived
// rather than deleted.
func retiredByArchive(state string) bool {
	switch state {
	case "APPROVED", "ACCEPTED", "IN_REVIEW", "WAITING_FOR_REVIEW", "READY_FOR_DISTRIBUTION", "COMPLETE":
		return true
	}
	return false
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	if c == nil {
		c = []string{}
	}
	return c
}

func (p *planOut) preview() tool.PlanPreview {
	pv := tool.PlanPreview{
		Summary: fmt.Sprintf("%s %s: upload %d, place %d, delete %d placement(s), reorder %d group(s), retire %d asset(s)",
			p.Target.Kind, p.Target.ID, len(p.Uploads), len(p.Create), len(p.Delete), len(p.Order), len(p.Archive)),
		Intended: map[string]any{"uploads": p.Uploads, "placements_create": p.Create, "placements_delete": p.Delete, "order": p.Order, "archive": p.Archive},
	}
	for _, d := range p.Delete {
		pv.Affected = append(pv.Affected, "DELETE /v1/appAssetLibraryPlacements/"+d.ID)
	}
	for _, a := range p.Archive {
		pv.Affected = append(pv.Affected, a.Action+" asset "+a.AssetID)
	}
	if len(p.Delete) > 0 {
		pv.Irreversible = append(pv.Irreversible, "deleted placements must be re-created to restore them")
	}
	if len(p.Archive) > 0 {
		pv.Irreversible = append(pv.Irreversible, "retired assets' placements are deleted first; unreviewed assets are deleted")
	}
	return pv
}

func (p *planOut) nextArgs(in planIn) map[string]any {
	args := map[string]any{"app": p.App, "target": p.Target.Kind}
	if in.ID != "" {
		args["id"] = in.ID
	}
	if in.Platform != "" {
		args["platform"] = string(in.Platform)
	}
	if in.Dir != "" {
		args["dir"] = in.Dir
	}
	if p.Mode != modeAdd {
		args["mode"] = p.Mode
	}
	if len(in.Remove) > 0 {
		args["remove"] = in.Remove
	}
	if len(in.Archive) > 0 {
		args["archive"] = in.Archive
	}
	return args
}

func libraryPlan(ctx context.Context, env *tool.Env, in planIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in)
	if err != nil {
		return nil, err
	}
	if p.Status != "ready" {
		return tool.OK(p), nil
	}
	fp, err := tool.Fingerprint(p.fp)
	if err != nil {
		return nil, err
	}
	id, err := env.Plans.Propose(ctx, "library_apply", p.key, fp, p.preview())
	if err != nil {
		return nil, fmt.Errorf("propose library_apply: %w", err)
	}
	p.PlanID = id
	args := p.nextArgs(in)
	args["plan_id"] = id
	return tool.OK(p).Then(tool.NextStep{Tool: "library_apply", Args: args}), nil
}

// stepOut is the outcome of one apply step.
type stepOut struct {
	Step   string `json:"step"`
	File   string `json:"file,omitempty"`
	ID     string `json:"id,omitempty"`
	State  string `json:"state,omitempty"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"`
	Hint   string `json:"hint,omitempty"`
	Status string `json:"status"` // done, already, pending, failed, not_attempted
}

type applyOut struct {
	Status string    `json:"status"`
	Steps  []stepOut `json:"steps"`
}

func libraryApply(ctx context.Context, env *tool.Env, in applyIn) (*tool.Result, error) {
	if in.PlanID == "" {
		return nil, tool.WithNext(tool.InvalidArgs("plan_id is required: run library_plan first"), tool.NextStep{Tool: "library_plan"})
	}
	p, err := computePlan(ctx, env, in.planIn)
	if err != nil {
		return nil, err
	}
	replan := tool.NextStep{Tool: "library_plan", Args: p.nextArgs(in.planIn)}
	switch p.Status {
	case "invalid":
		pr := p.Problems[0]
		return nil, tool.WithNext(tool.InvalidArgs("%d problem(s), first: %s %s: %s", len(p.Problems), pr.File, pr.Kind, pr.Message), replan)
	case "no_changes":
		return tool.Already(p), nil
	}
	fp, err := tool.Fingerprint(p.fp)
	if err != nil {
		return nil, err
	}
	gate := tool.Preview
	if p.human {
		gate = tool.PreviewHuman
	}
	wctx, res := gate(ctx, env, in.PlanID, p.key, fp, p.preview())
	if res != nil {
		return res, nil
	}
	a := applier{env: env, p: p, out: &applyOut{Steps: []stepOut{}}, ids: map[string]string{}}
	a.run(wctx)
	return a.result(replan), nil
}

type applier struct {
	env     *tool.Env
	p       *planOut
	out     *applyOut
	ids     map[string]string // new:md5 → asset id / placement placeholder → id
	stopped error
	failed  int
}

func (a *applier) step(s stepOut, err error) {
	if err != nil {
		b := tool.BodyFor(err)
		s.Status, s.Error, s.Code = "failed", b.Message, b.Error
		if b.Hint != "" {
			s.Hint = b.Hint
		}
		var api *tool.APIError
		if errors.As(err, &api) && strings.Contains(api.Code, "ASSET_HAS_PLACEMENTS") {
			s.Hint = "the asset still has placements (possibly on another version, page or event); delete them, then run library_plan again"
		}
		a.failed++
		if errors.Is(err, tool.ErrOutcomeUnknown) || tool.APIStatus(err) >= 500 || errors.Is(err, context.Canceled) {
			a.stopped = err
		}
	}
	a.out.Steps = append(a.out.Steps, s)
}

func (a *applier) skip(s stepOut) {
	s.Status = "not_attempted"
	a.out.Steps = append(a.out.Steps, s)
}

func (a *applier) run(ctx context.Context) {
	env, p := a.env, a.p
	// 1. Uploads.
	for _, u := range p.Uploads {
		s := stepOut{Step: "upload", File: u.File}
		if a.stopped != nil {
			a.skip(s)
			continue
		}
		r, err := a.upload(ctx, u)
		if r != nil && r.ID != "" {
			a.ids["new:"+u.MD5] = r.ID
			s.ID, s.State = r.ID, r.State
			if err == nil {
				rememberAsset(env, stateKey(env, p.App), u.MD5, r.ID)
			}
		}
		if err == nil && r != nil && r.StillProcessing {
			s.Status = "pending"
			a.out.Steps = append(a.out.Steps, s)
			continue
		}
		if err == nil {
			s.Status = "done"
		}
		a.step(s, err)
	}
	// 2–3. Create and delete placements, one locale group at a time.
	for _, g := range a.groupWork() {
		a.applyGroup(ctx, g)
	}
	// 4. Reorder.
	for _, o := range p.Order {
		s := stepOut{Step: "reorder", ID: o.Locale + "/" + o.Group}
		ids, ok := a.orderIDs(o)
		if a.stopped != nil || !ok {
			a.skip(s)
			continue
		}
		_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/appAssetLibraryPlacementOrderingRequests", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":       "appAssetLibraryPlacementOrderingRequests",
				"attributes": map[string]any{"placementGroup": o.Group},
				"relationships": map[string]any{
					p.Target.locRel:     map[string]any{"data": map[string]any{"type": p.Target.locType, "id": o.locID}},
					"orderedPlacements": map[string]any{"data": refs("appAssetLibraryPlacements", ids)},
				},
			},
		}})
		if err == nil {
			s.Status = "done"
		}
		a.step(s, err)
	}
	// 5. Retire assets: placements first, then delete or archive.
	for _, r := range p.Archive {
		s := stepOut{Step: r.Action + "_asset", ID: r.AssetID}
		if a.stopped != nil {
			a.skip(s)
			continue
		}
		err := a.retire(ctx, r)
		if err == nil {
			s.Status = "done"
		}
		a.step(s, err)
	}
}

// assetLocks serializes rememberAsset's load→save per state key within this
// process (a concurrent apply in another process can still race).
var assetLocks sync.Map // key → *sync.Mutex

// rememberAsset records md5 → asset id in the reuse index. Under the key's
// in-process lock it reloads the index right before saving and sets only
// this entry, so entries saved by an overlapping apply survive.
func rememberAsset(env *tool.Env, key, sum, id string) {
	mu, _ := assetLocks.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	var index map[string]string
	if _, err := env.State.Load(stateKind, key, &index); err != nil || index == nil {
		index = map[string]string{}
	}
	index[sum] = id
	_ = env.State.Save(stateKind, key, index)
}

// group is the work of one locale's placement group: indexes into Create,
// explicit removes and replaced placements.
type group struct {
	info     groupInfo
	creates  []int
	removes  []deleteOut
	replaces []deleteOut
}

// groupWork splits the plan's creates and deletes by locale group, in plan
// order.
func (a *applier) groupWork() []*group {
	var order []*group
	by := map[string]*group{}
	get := func(k string) *group {
		g := by[k]
		if g == nil {
			g = &group{info: a.p.groups[k]}
			by[k] = g
			order = append(order, g)
		}
		return g
	}
	for i, c := range a.p.Create {
		g := get(gkey(c.Locale, c.Type, c.Group))
		g.creates = append(g.creates, i)
	}
	for _, d := range a.p.Delete {
		g := get(gkey(d.Locale, d.typ, d.Group))
		if d.Why == "replace" {
			g.replaces = append(g.replaces, d)
		} else {
			g.removes = append(g.removes, d)
		}
	}
	return order
}

// applyGroup runs one group. Explicit removes go first. A replaced placement
// is deleted before a create only when the group is at Apple's limit; the
// rest are deleted only after every create of the group landed, so a failed
// or skipped create never leaves the group short. After a create fails, no
// further placement of the group is deleted.
func (a *applier) applyGroup(ctx context.Context, g *group) {
	const keptHint = "old placement kept: a new placement of this group did not land; run library_plan again"
	cur := g.info.have
	for _, d := range g.removes {
		if a.deletePlacement(ctx, d) {
			cur--
		}
	}
	landed := true
	for _, i := range g.creates {
		c := a.p.Create[i]
		s := stepOut{Step: "place", File: c.File}
		asset := c.Asset
		if strings.HasPrefix(asset, "new:") {
			id, ok := a.ids[asset]
			if !ok || !a.uploadReady(asset) {
				landed = false
				a.skip(s)
				continue
			}
			asset = id
		}
		if a.stopped != nil {
			landed = false
			a.skip(s)
			continue
		}
		if g.info.limit > 0 && cur >= g.info.limit {
			if !landed || len(g.replaces) == 0 {
				landed = false
				s.Hint = "the group is at Apple's limit and no old placement may be deleted; run library_plan again"
				a.skip(s)
				continue
			}
			d := g.replaces[0]
			g.replaces = g.replaces[1:]
			if !a.deletePlacement(ctx, d) {
				landed = false
				s.Hint = "the group is at Apple's limit and the old placement could not be deleted; run library_plan again"
				a.skip(s)
				continue
			}
			cur--
		}
		id, already, err := a.place(ctx, c, asset)
		if err == nil {
			s.ID, s.Status = id, "done"
			if already {
				s.Status = "already"
			}
			a.ids[fmt.Sprintf("place:%d", i)] = id
			cur++
		} else {
			landed = false
		}
		a.step(s, err)
	}
	for _, d := range g.replaces {
		if !landed {
			a.skip(stepOut{Step: "delete_placement", ID: d.ID, Hint: keptHint})
			continue
		}
		a.deletePlacement(ctx, d)
	}
}

// deletePlacement deletes one placement, reporting success.
func (a *applier) deletePlacement(ctx context.Context, d deleteOut) bool {
	s := stepOut{Step: "delete_placement", ID: d.ID}
	if a.stopped != nil {
		a.skip(s)
		return false
	}
	err := a.del(ctx, "/v1/appAssetLibraryPlacements/"+d.ID)
	if err == nil {
		s.Status = "done"
	}
	a.step(s, err)
	return err == nil
}

func refs(typ string, ids []string) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"type": typ, "id": id})
	}
	return out
}

// orderIDs resolves an ordering request's placeholders; false when a
// placement of the group was not created.
func (a *applier) orderIDs(o orderOut) ([]string, bool) {
	ids := make([]string, 0, len(o.IDs))
	for _, id := range o.IDs {
		if !strings.HasPrefix(id, "new:") && !a.isPlannedAsset(id) {
			ids = append(ids, id)
			continue
		}
		found := ""
		for i, c := range a.p.Create {
			if c.Asset == id && c.Group == o.Group && c.Locale == o.Locale {
				found = a.ids[fmt.Sprintf("place:%d", i)]
			}
		}
		if found == "" {
			return nil, false
		}
		ids = append(ids, found)
	}
	return ids, true
}

// isPlannedAsset reports an id in an ordering list that is an existing asset
// to be placed (not a placement id).
func (a *applier) isPlannedAsset(id string) bool {
	for _, c := range a.p.Create {
		if c.Asset == id {
			return true
		}
	}
	return false
}

func (a *applier) uploadReady(placeholder string) bool {
	for _, s := range a.out.Steps {
		if s.Step == "upload" && s.ID == a.ids[placeholder] {
			return s.Status == "done"
		}
	}
	return false
}

func (a *applier) del(ctx context.Context, p string) error {
	_, err := a.env.ASC.Do(ctx, tool.Request{Method: http.MethodDelete, Path: p, Write: true})
	if tool.IsNotFound(err) {
		return nil
	}
	return err
}

func (a *applier) place(ctx context.Context, c placementOut, asset string) (string, bool, error) {
	rel, typ := "image", "appAssetLibraryImages"
	if a.p.assetKinds[c.Asset] == "video" {
		rel, typ = "video", "appAssetLibraryVideos"
	}
	resp, err := a.env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/appAssetLibraryPlacements", Write: true, Body: map[string]any{
		"data": map[string]any{
			"type":       "appAssetLibraryPlacements",
			"attributes": map[string]any{"placementType": c.Type, "placementGroup": c.Group},
			"relationships": map[string]any{
				rel:               map[string]any{"data": map[string]any{"type": typ, "id": asset}},
				a.p.Target.locRel: map[string]any{"data": map[string]any{"type": a.p.Target.locType, "id": c.locID}},
			},
		},
	}})
	if err != nil {
		if tool.IsConflict(err) {
			// Read back: an identical placement means a previous run landed it.
			ps, rerr := readPlacements(ctx, a.env, a.p.Target.locType, c.locID)
			if rerr == nil {
				for _, p := range ps {
					if p.Asset == asset && p.Group == c.Group && p.Type == c.Type {
						return p.ID, true, nil
					}
				}
			}
		}
		return "", false, err
	}
	r, _ := resp.First()
	return r.ID, false, nil
}

func (a *applier) retire(ctx context.Context, r archiveOut) error {
	typ := "appAssetLibraryImages"
	if r.Kind == "video" {
		typ = "appAssetLibraryVideos"
	}
	ps, err := a.env.ASC.List(ctx, "/v1/"+typ+"/"+r.AssetID+"/placements", tool.ListOptions{MaxItems: 1000})
	if err != nil {
		return fmt.Errorf("read placements of asset %s: %w", r.AssetID, err)
	}
	for _, p := range ps.Data {
		if err := a.del(ctx, "/v1/appAssetLibraryPlacements/"+p.ID); err != nil {
			return fmt.Errorf("delete placement %s: %w", p.ID, err)
		}
	}
	if r.Action == "archive" {
		_, err = a.env.ASC.Do(ctx, tool.Request{Method: http.MethodPatch, Path: "/v1/" + typ + "/" + r.AssetID, Write: true, Body: map[string]any{
			"data": map[string]any{"type": typ, "id": r.AssetID, "attributes": map[string]any{"archived": true}},
		}})
		return err
	}
	return a.del(ctx, "/v1/"+typ+"/"+r.AssetID)
}

func (a *applier) result(replan tool.NextStep) *tool.Result {
	done, pending := 0, 0
	for _, s := range a.out.Steps {
		switch s.Status {
		case "done", "already":
			done++
		case "pending":
			pending++
		}
	}
	switch {
	case a.failed > 0 && done == 0 && pending == 0:
		a.out.Status = "failed"
		if a.stopped != nil && (errors.Is(a.stopped, tool.ErrOutcomeUnknown) || tool.APIStatus(a.stopped) >= 500) {
			return tool.Fail(tool.WithNext(fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, a.stopped), replan))
		}
		return tool.Fail(tool.WithNext(fmt.Errorf("library_apply: %s", a.out.Steps[0].Error), replan))
	case a.failed > 0 || pending > 0 || a.notAttempted():
		a.out.Status = "partial"
		return tool.OK(a.out).Warn("some steps did not complete; run library_plan again to see what remains").Then(replan)
	}
	a.out.Status = "applied"
	return tool.OK(a.out)
}

func (a *applier) notAttempted() bool {
	for _, s := range a.out.Steps {
		if s.Status == "not_attempted" {
			return true
		}
	}
	return false
}
