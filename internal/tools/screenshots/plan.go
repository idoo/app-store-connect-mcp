package screenshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for image.DecodeConfig
	_ "image/png"  // register PNG for image.DecodeConfig
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/upload"
)

// Sync modes.
const (
	modeReplace = "replace"
	modeAdd     = "add"
)

// Plan statuses.
const (
	statusPlanned   = "planned"
	statusNoChanges = "no_changes"
	statusInvalid   = "invalid"
)

// Problem kinds reported by screenshots_plan.
const (
	problemInvalidImage        = "invalid_image"
	problemAmbiguousSize       = "ambiguous_size"
	problemUnknownSize         = "unknown_size"
	problemUnknownDisplay      = "unknown_display"
	problemUnknownLocale       = "unknown_locale"
	problemMissingLocalization = "missing_localization"
	problemTooMany             = "too_many_screenshots"
	problemLayout              = "unexpected_layout"
)

// sourceIn is shared by screenshots_plan and screenshots_sync.
type sourceIn struct {
	App      tool.AppRef     `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Platform tool.Platform   `json:"platform,omitempty" jsonschema:"default: inferred from the app"`
	Version  tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default) or a version string of an editable version"`
	Dir      string          `json:"dir" jsonschema:"screenshots directory inside the project: <locale>/*.png|jpg (fastlane; display inferred from pixels) or <locale>/<display>/*.png"`
	Mode     string          `json:"mode,omitempty" jsonschema:"replace (default): each touched set becomes exactly the local files, extra remote screenshots are deleted; add: only upload missing ones"`
	Locales  tool.LocaleSet  `json:"locales,omitempty" jsonschema:"only these locales of the directory (default all)"`
}

var modeEnum = []any{modeReplace, modeAdd}

// localFile is one validated local screenshot.
type localFile struct {
	path     string
	Rel      string                `json:"file"`
	Locale   string                `json:"-"`
	Display  applemeta.DisplayType `json:"-"`
	Width    int                   `json:"width"`
	Height   int                   `json:"height"`
	Size     int64                 `json:"size"`
	Checksum string                `json:"checksum"`
}

// problem is a local file or directory that blocks the plan.
type problem struct {
	File       string                  `json:"file"`
	Kind       string                  `json:"kind"`
	Message    string                  `json:"message"`
	Candidates []applemeta.DisplayType `json:"candidates,omitempty"`
}

type skipItem struct {
	File string `json:"file"`
	ID   string `json:"id"`
}

type deleteItem struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	Checksum string `json:"checksum,omitempty"`
}

// slot is one position of the desired order: an existing screenshot id or
// an index into setPlan.Uploads.
type slot struct {
	id     string
	upload int
}

// setPlan is the planned change of one (locale, display) screenshot set.
type setPlan struct {
	Locale         string                `json:"locale"`
	LocalizationID string                `json:"localization_id"`
	Display        applemeta.DisplayType `json:"display"`
	SetID          string                `json:"set_id,omitempty"`
	CreateSet      bool                  `json:"create_set,omitempty"`
	Uploads        []localFile           `json:"uploads"`
	Skips          []skipItem            `json:"skips"`
	Deletes        []deleteItem          `json:"deletes"`
	Reorder        bool                  `json:"reorder,omitempty"`
	Order          []string              `json:"order"`
	remoteCount    int
	order          []slot
}

func (s *setPlan) changed() bool {
	return s.CreateSet || len(s.Uploads) > 0 || len(s.Deletes) > 0 || s.Reorder
}

type summaryOut struct {
	Sets       int `json:"sets"`
	CreateSets int `json:"create_sets"`
	Uploads    int `json:"uploads"`
	Skips      int `json:"skips"`
	Deletes    int `json:"deletes"`
	Reorders   int `json:"reorders"`
}

type planOut struct {
	Status   string        `json:"status"`
	App      appOut        `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionOut    `json:"version"`
	Mode     string        `json:"mode"`
	Summary  summaryOut    `json:"summary"`
	Sets     []*setPlan    `json:"sets"`
	Problems []problem     `json:"problems,omitempty"`
	PlanID   string        `json:"plan_id,omitempty"`
}

// syncKey is the canonical argument set a screenshots_sync plan is bound to.
type syncKey struct {
	App       string   `json:"app"`
	Platform  string   `json:"platform"`
	VersionID string   `json:"version_id"`
	Dir       string   `json:"dir"`
	Mode      string   `json:"mode"`
	Locales   []string `json:"locales,omitempty"`
	Local     string   `json:"local_sha256"`
}

// planned is a computed screenshot plan.
type planned struct {
	t        *target
	in       sourceIn
	mode     string
	sets     []*setPlan
	problems []problem
	remote   any
	key      syncKey
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "screenshots_plan", Toolset: tool.Screenshots, Class: tool.Read, Idempotent: true,
		Title: "Plan screenshot sync",
		Description: "Diffs a local screenshot dir against the editable version: validates PNG/JPEG size and alpha, skips equal " +
			"checksums, lists uploads, deletes (replace mode), reorders and problems. Returns plan_id for screenshots_sync. " +
			"Ambiguous sizes need a <display> subdir.",
		Enums: map[string][]any{"platform": platformEnum, "mode": modeEnum},
	}, screenshotsPlan))
}

func screenshotsPlan(ctx context.Context, env *tool.Env, in sourceIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in)
	if err != nil {
		return nil, err
	}
	out := p.out()
	switch out.Status {
	case statusInvalid:
		res := tool.OK(out).Warn("fix the problems and plan again; no plan was issued")
		if slices.ContainsFunc(p.problems, func(pr problem) bool { return pr.Kind == problemMissingLocalization }) {
			res.Then(tool.NextStep{Tool: "metadata_plan", Args: map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform)}})
		}
		return res, nil
	case statusNoChanges:
		return tool.OK(out), nil
	}
	fp, err := p.fingerprint()
	if err != nil {
		return nil, err
	}
	id, err := env.Plans.Propose(ctx, "screenshots_sync", p.key, fp, p.preview())
	if err != nil {
		return nil, fmt.Errorf("propose screenshots_sync: %w", err)
	}
	out.PlanID = id
	args := p.nextArgs()
	args["confirm"] = id
	return tool.OK(out).Then(tool.NextStep{Tool: "screenshots_sync", Args: args}), nil
}

// computePlan reads the local directory and the remote sets and diffs them.
func computePlan(ctx context.Context, env *tool.Env, in sourceIn) (*planned, error) {
	mode := in.Mode
	if mode == "" {
		mode = modeReplace
	}
	if mode != modeReplace && mode != modeAdd {
		return nil, fmt.Errorf("%w: mode must be replace or add", tool.ErrInvalidArgs)
	}
	if strings.TrimSpace(in.Dir) == "" {
		return nil, fmt.Errorf("%w: dir is required", tool.ErrInvalidArgs)
	}
	root, err := env.Files.Resolve(in.Dir)
	if err != nil {
		return nil, fmt.Errorf("%w: dir: %v", tool.ErrInvalidArgs, err)
	}
	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, true)
	if err != nil {
		return nil, err
	}
	var only []string
	if len(in.Locales) > 0 && !in.Locales.IsAll() {
		for _, raw := range in.Locales {
			code, err := applemeta.NormalizeLocale(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
			}
			only = append(only, code)
		}
		slices.Sort(only)
		only = slices.Compact(only)
	}
	files, problems, err := scanDir(env, root, t.Platform, only)
	if err != nil {
		return nil, err
	}
	p := &planned{t: t, in: in, mode: mode, problems: problems}

	locs, err := localizations(ctx, env, t.Version.ID)
	if err != nil {
		return nil, err
	}
	byLocale := map[string][]localFile{}
	for _, f := range files {
		byLocale[f.Locale] = append(byLocale[f.Locale], f)
	}
	codes := make([]string, 0, len(byLocale))
	for code := range byLocale {
		codes = append(codes, code)
	}
	slices.Sort(codes)

	type remoteState struct {
		Locale         string      `json:"locale"`
		LocalizationID string      `json:"localization_id"`
		Sets           []remoteSet `json:"sets"`
	}
	var remote []remoteState
	for _, code := range codes {
		loc, ok := locs[code]
		if !ok {
			p.problems = append(p.problems, problem{
				File: code, Kind: problemMissingLocalization,
				Message: fmt.Sprintf("version %s has no %s localization; add it with metadata_plan first", t.Version.String, code),
			})
			continue
		}
		sets, err := screenshotSets(ctx, env, loc.ID)
		if err != nil {
			return nil, err
		}
		remote = append(remote, remoteState{Locale: code, LocalizationID: loc.ID, Sets: fingerprintSets(sets)})
		groups := map[applemeta.DisplayType][]localFile{}
		for _, f := range byLocale[code] {
			groups[f.Display] = append(groups[f.Display], f)
		}
		displays := make([]applemeta.DisplayType, 0, len(groups))
		for d := range groups {
			displays = append(displays, d)
		}
		slices.Sort(displays)
		for _, d := range displays {
			var rs *remoteSet
			for i := range sets {
				if sets[i].Display == d {
					rs = &sets[i]
					break
				}
			}
			sp, prob := diffSet(code, loc.ID, d, groups[d], rs, mode)
			if prob != nil {
				p.problems = append(p.problems, *prob)
				continue
			}
			p.sets = append(p.sets, sp)
		}
	}
	p.remote = remote

	digest := make([]string, 0, len(files))
	for _, f := range files {
		digest = append(digest, f.Locale+"/"+string(f.Display)+"/"+f.Rel+"="+f.Checksum)
	}
	sum := sha256.Sum256([]byte(strings.Join(digest, "\n")))
	p.key = syncKey{
		App: t.App.ID, Platform: string(t.Platform), VersionID: t.Version.ID, Dir: root, Mode: mode,
		Locales: only, Local: hex.EncodeToString(sum[:]),
	}
	return p, nil
}

// fingerprintSets strips presentation-only data so only ids, checksums and
// order feed the fingerprint.
func fingerprintSets(sets []remoteSet) []remoteSet {
	out := make([]remoteSet, len(sets))
	for i, s := range sets {
		out[i] = remoteSet{ID: s.ID, Display: s.Display, Shots: make([]remoteShot, len(s.Shots))}
		for j, sh := range s.Shots {
			out[i].Shots[j] = remoteShot{ID: sh.ID, Checksum: sh.Checksum, Order: sh.Order}
		}
	}
	return out
}

// scanDir walks root: <locale>/<file> or <locale>/<display>/<file>.
func scanDir(env *tool.Env, root string, platform tool.Platform, only []string) ([]localFile, []problem, error) {
	var files []localFile
	var problems []problem
	badLocales := map[string]bool{}
	err := env.Files.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && path != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative path of %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		parts := strings.Split(rel, "/")
		if len(parts) < 2 || len(parts) > 3 {
			problems = append(problems, problem{File: rel, Kind: problemLayout, Message: "expected <locale>/<file> or <locale>/<display>/<file>"})
			return nil
		}
		locale, err := applemeta.NormalizeLocale(parts[0])
		if err != nil {
			if !badLocales[parts[0]] {
				badLocales[parts[0]] = true
				problems = append(problems, problem{File: parts[0], Kind: problemUnknownLocale, Message: err.Error()})
			}
			return nil
		}
		if only != nil && !slices.Contains(only, locale) {
			return nil
		}
		var display applemeta.DisplayType
		if len(parts) == 3 {
			if display, err = applemeta.ParseDisplay(parts[1]); err != nil {
				problems = append(problems, problem{File: rel, Kind: problemUnknownDisplay, Message: err.Error()})
				return nil
			}
		}
		f, prob, err := inspect(env, path, rel, locale, display, platform)
		if err != nil {
			return err
		}
		if prob != nil {
			problems = append(problems, *prob)
			return nil
		}
		files = append(files, *f)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read %s: %v", tool.ErrInvalidArgs, root, err)
	}
	slices.SortFunc(files, func(a, b localFile) int { return strings.Compare(a.Rel, b.Rel) })
	return files, problems, nil
}

// inspect reads one image: pixel size → display (unless given), validation
// (format, alpha, exact size) and MD5.
func inspect(env *tool.Env, path, rel, locale string, display applemeta.DisplayType, platform tool.Platform) (*localFile, *problem, error) {
	fh, err := env.Files.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer func() { _ = fh.Close() }()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return nil, &problem{File: rel, Kind: problemInvalidImage, Message: "not a decodable PNG or JPEG: " + err.Error()}, nil
	}
	if display == "" {
		cands, _ := applemeta.DisplayForPixels(cfg.Width, cfg.Height, platform)
		switch len(cands) {
		case 0:
			return nil, &problem{File: rel, Kind: problemUnknownSize, Message: fmt.Sprintf("%dx%d matches no %s display type", cfg.Width, cfg.Height, platform)}, nil
		case 1:
			display = cands[0]
		default:
			return nil, &problem{
				File: rel, Kind: problemAmbiguousSize, Candidates: cands,
				Message: fmt.Sprintf("%dx%d fits several display types; move it into %s/<display>/", cfg.Width, cfg.Height, locale),
			}, nil
		}
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("rewind %s: %w", rel, err)
	}
	if err := upload.ValidateImage(fh, display); err != nil {
		return nil, &problem{File: rel, Kind: problemInvalidImage, Message: strings.TrimPrefix(err.Error(), tool.ErrInvalidArgs.Message+": ")}, nil
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("rewind %s: %w", rel, err)
	}
	sum, err := upload.FileMD5(fh)
	if err != nil {
		return nil, nil, fmt.Errorf("checksum %s: %w", rel, err)
	}
	st, err := fh.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", rel, err)
	}
	return &localFile{
		path: path, Rel: rel, Locale: locale, Display: display,
		Width: cfg.Width, Height: cfg.Height, Size: st.Size(), Checksum: sum,
	}, nil, nil
}

// diffSet plans one set. Local files are already in file-name order.
func diffSet(locale, locID string, d applemeta.DisplayType, local []localFile, rs *remoteSet, mode string) (*setPlan, *problem) {
	sp := &setPlan{
		Locale: locale, LocalizationID: locID, Display: d,
		Uploads: []localFile{}, Skips: []skipItem{}, Deletes: []deleteItem{}, Order: []string{},
	}
	var remote []remoteShot
	if rs == nil {
		sp.CreateSet = true
	} else {
		sp.SetID, remote = rs.ID, rs.Shots
	}
	sp.remoteCount = len(remote)
	used := map[string]bool{}
	var localOrder []slot
	var labels []string
	for _, f := range local {
		i := slices.IndexFunc(remote, func(r remoteShot) bool { return !used[r.ID] && r.Checksum != "" && r.Checksum == f.Checksum })
		if i >= 0 {
			used[remote[i].ID] = true
			sp.Skips = append(sp.Skips, skipItem{File: f.Rel, ID: remote[i].ID})
			localOrder = append(localOrder, slot{id: remote[i].ID, upload: -1})
		} else {
			sp.Uploads = append(sp.Uploads, f)
			localOrder = append(localOrder, slot{upload: len(sp.Uploads) - 1})
		}
		labels = append(labels, f.Rel)
	}
	// Natural order after the mutations: remaining remote screenshots in
	// their current order, then uploads appended in local order.
	var natural []slot
	var naturalLabels []string
	for _, r := range remote {
		if mode == modeReplace && !used[r.ID] {
			sp.Deletes = append(sp.Deletes, deleteItem{ID: r.ID, FileName: r.FileName, Checksum: r.Checksum})
			continue
		}
		natural = append(natural, slot{id: r.ID, upload: -1})
		naturalLabels = append(naturalLabels, remoteLabel(r, local, used))
	}
	for i, f := range sp.Uploads {
		natural = append(natural, slot{upload: i})
		naturalLabels = append(naturalLabels, f.Rel)
	}
	if mode == modeReplace {
		sp.order, sp.Order = localOrder, labels
	} else {
		sp.order, sp.Order = natural, naturalLabels
	}
	sp.Reorder = !slices.Equal(sp.order, natural)
	if len(sp.order) > maxScreenshotsPerSet {
		return nil, &problem{
			File: locale + "/" + string(d), Kind: problemTooMany,
			Message: fmt.Sprintf("set would hold %d screenshots; App Store Connect allows %d", len(sp.order), maxScreenshotsPerSet),
		}
	}
	return sp, nil
}

// remoteLabel names a kept remote screenshot by its matching local file, else
// by its remote file name.
func remoteLabel(r remoteShot, local []localFile, used map[string]bool) string {
	if used[r.ID] {
		for _, f := range local {
			if f.Checksum == r.Checksum {
				return f.Rel
			}
		}
	}
	return "remote:" + r.FileName + " (" + r.ID + ")"
}

func (p *planned) out() planOut {
	o := planOut{
		App: p.t.appOut(), Platform: p.t.Platform, Version: p.t.versionOut(), Mode: p.mode,
		Sets: []*setPlan{}, Problems: p.problems,
	}
	for _, s := range p.sets {
		o.Summary.Sets++
		if s.CreateSet {
			o.Summary.CreateSets++
		}
		if s.Reorder {
			o.Summary.Reorders++
		}
		o.Summary.Uploads += len(s.Uploads)
		o.Summary.Skips += len(s.Skips)
		o.Summary.Deletes += len(s.Deletes)
		o.Sets = append(o.Sets, s)
	}
	switch {
	case len(p.problems) > 0:
		o.Status = statusInvalid
	case !p.changed():
		o.Status = statusNoChanges
	default:
		o.Status = statusPlanned
	}
	return o
}

func (p *planned) changed() bool {
	return slices.ContainsFunc(p.sets, (*setPlan).changed)
}

func (p *planned) deletes() int {
	n := 0
	for _, s := range p.sets {
		n += len(s.Deletes)
	}
	return n
}

// fingerprint digests the remote sets the plan depends on.
func (p *planned) fingerprint() (string, error) {
	fp, err := tool.Fingerprint(p.remote)
	if err != nil {
		return "", fmt.Errorf("fingerprint: %w", err)
	}
	return fp, nil
}

// preview renders the approval preview.
func (p *planned) preview() tool.PlanPreview {
	o := p.out()
	pv := tool.PlanPreview{
		Summary: fmt.Sprintf("sync screenshots of %s %s %s (%s): %d upload(s), %d delete(s), %d new set(s), %d reorder(s)",
			p.t.App.Name, p.t.Platform, p.t.Version.String, p.mode, o.Summary.Uploads, o.Summary.Deletes, o.Summary.CreateSets, o.Summary.Reorders),
	}
	current := map[string]int{}
	intended := map[string][]string{}
	for _, s := range p.sets {
		if !s.changed() {
			continue
		}
		k := s.Locale + " " + string(s.Display)
		current[k] = s.remoteCount
		intended[k] = s.Order
		pv.Affected = append(pv.Affected, k)
		for _, d := range s.Deletes {
			pv.Irreversible = append(pv.Irreversible, fmt.Sprintf("delete %s %s (%s)", k, d.FileName, d.ID))
		}
	}
	pv.Current, pv.Intended = current, intended
	return pv
}

// nextArgs echoes the caller's arguments with resolved ids.
func (p *planned) nextArgs() map[string]any {
	args := map[string]any{
		"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String,
		"dir": p.in.Dir, "mode": p.mode,
	}
	if p.in.Version == "" {
		args["version"] = string(tool.VersionEditable)
	}
	if len(p.in.Locales) > 0 {
		args["locales"] = []string(p.in.Locales)
	}
	return args
}
