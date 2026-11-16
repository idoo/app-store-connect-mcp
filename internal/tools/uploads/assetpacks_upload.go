package uploads

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/upload"
)

// Asset pack version states (BackgroundAssetVersion.state).
const (
	packAwaitingUpload = "AWAITING_UPLOAD"
	packProcessing     = "PROCESSING"
	packComplete       = "COMPLETE"
	packFailed         = "FAILED"
)

const (
	packWaitDefault  = 300 * time.Second
	packWaitMax      = 300 * time.Second
	packPollInterval = 5 * time.Second
	packFilesMax     = 50
)

type packUploadIn struct {
	App  tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Pack string      `json:"pack" jsonschema:"asset pack identifier, equal to assetPackID in the archive's manifest"`
	File string      `json:"file" jsonschema:".aar archive made by xcrun ba-package, inside the project"`
	tool.ConfirmFlag
	Wait        *bool `json:"wait,omitempty" jsonschema:"wait for Apple's processing (default true)"`
	WaitSeconds int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for processing (default 300, max 300)"`
}

type packRefOut struct {
	ID         string `json:"id,omitempty"`
	Identifier string `json:"asset_pack_identifier"`
	Created    bool   `json:"created,omitempty"`
}

type packUploadOut struct {
	App           map[string]any `json:"app"`
	Pack          packRefOut     `json:"pack"`
	Version       *packVerOut    `json:"version,omitempty"`
	UploadFileID  string         `json:"upload_file_id,omitempty"`
	File          string         `json:"file"`
	SizeBytes     int64          `json:"size_bytes"`
	UploadedBytes int64          `json:"uploaded_bytes"`
	Resumed       bool           `json:"resumed,omitempty"`
	Already       bool           `json:"already,omitempty"`
	Hint          string         `json:"hint,omitempty"`
}

// packPlan is what an upload call would do, decided from reads only.
type packPlan struct {
	packID  string     // "" = create the pack
	version packVerOut // reused version when reuse is set
	reuse   bool       // newest version is AWAITING_UPLOAD
	fileID  string     // matching reservation on the reused version
	already bool       // the file is already uploaded to the newest version
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "asset_packs_upload", Toolset: tool.Uploads, Class: tool.Write, Confirm: tool.ConfirmByFlag,
		Title: "Upload a Background Assets asset pack (.aar)",
		Description: "Uploads an .aar (xcrun ba-package) as an asset pack version; pack must equal its manifest assetPackID (unchecked). " +
			"A new pack id needs a human. Same file again resumes. COMPLETE serves internal TestFlight; external and App Store need review.",
	}, assetPacksUpload))
}

func assetPacksUpload(ctx context.Context, env *tool.Env, in packUploadIn) (*tool.Result, error) {
	in.Pack = strings.TrimSpace(in.Pack)
	switch {
	case in.Pack == "":
		return nil, fmt.Errorf("%w: pack is required", tool.ErrInvalidArgs)
	case strings.ContainsAny(in.Pack, " \t\r\n/"):
		return nil, fmt.Errorf("%w: pack is the asset pack identifier (e.g. supertonic-3), not a path or name", tool.ErrInvalidArgs)
	case in.File == "":
		return nil, fmt.Errorf("%w: file is required", tool.ErrInvalidArgs)
	case !strings.EqualFold(filepath.Ext(in.File), ".aar"):
		return nil, fmt.Errorf("%w: %s is not an .aar archive (make it with xcrun ba-package)", tool.ErrInvalidArgs, in.File)
	}
	fh, err := env.Files.Open(in.File)
	if err != nil {
		return nil, fmt.Errorf("%w: file: %v", tool.ErrInvalidArgs, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", in.File, err)
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 {
		return nil, fmt.Errorf("%w: %s is not a non-empty regular file", tool.ErrInvalidArgs, in.File)
	}
	name, size := filepath.Base(in.File), st.Size()

	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	plan, err := planPackUpload(ctx, env, app.ID, in.Pack, name, size)
	if err != nil {
		return tool.Fail(err), nil
	}
	out := packUploadOut{
		App:  map[string]any{"id": app.ID, "name": tool.Untrusted(app.Name), "bundle_id": app.BundleID},
		Pack: packRefOut{ID: plan.packID, Identifier: in.Pack},
		File: in.File, SizeBytes: size,
	}
	wait := packWait(env, in)
	if plan.already {
		out.Already, out.Version, out.UploadFileID = true, &plan.version, plan.fileID
		return finishPackUpload(ctx, env, app.ID, out, wait)
	}

	wctx, gate := packGate(ctx, env, in, app, plan, name, size)
	if gate != nil {
		return gate, nil
	}

	// From here on every failure names what exists, so the next call resumes.
	if plan.packID == "" {
		res, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/backgroundAssets", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":          "backgroundAssets",
				"attributes":    map[string]any{"assetPackIdentifier": in.Pack},
				"relationships": map[string]any{"app": tool.RelData("apps", app.ID)},
			},
		}})
		if err != nil {
			return tool.Fail(fmt.Errorf("create asset pack %s: %w", in.Pack, err)), nil
		}
		r, _ := res.First()
		plan.packID = r.ID
		out.Pack.ID, out.Pack.Created = r.ID, true
	}
	ver := plan.version
	if !plan.reuse {
		res, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/backgroundAssetVersions", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":          "backgroundAssetVersions",
				"relationships": map[string]any{"backgroundAsset": tool.RelData("backgroundAssets", plan.packID)},
			},
		}})
		if err != nil {
			return tool.Fail(fmt.Errorf("asset pack %s (%s): create version: %w", in.Pack, plan.packID, err)), nil
		}
		r, _ := res.First()
		ver = shapeVersion(r, nil)
	}
	out.Version, out.Resumed = &ver, plan.reuse
	pending := func(step string, err error) (*tool.Result, error) {
		return tool.Fail(fmt.Errorf("asset pack %s version %s (%s) is still %s: %s (call again with the same file to resume): %w",
			in.Pack, ver.Version, ver.ID, packAwaitingUpload, step, err)), nil
	}

	var file tool.Resource
	if plan.fileID != "" {
		// Read the reservation by id: its upload URLs are signed afresh.
		res, err := env.ASC.Get(ctx, "/v1/backgroundAssetUploadFiles/"+url.PathEscape(plan.fileID), nil)
		if err != nil {
			return pending("read upload file "+plan.fileID, err)
		}
		file, _ = res.First()
	} else {
		res, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/backgroundAssetUploadFiles", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":          "backgroundAssetUploadFiles",
				"attributes":    map[string]any{"assetType": "ASSET", "fileName": name, "fileSize": size},
				"relationships": map[string]any{"backgroundAssetVersion": tool.RelData("backgroundAssetVersions", ver.ID)},
			},
		}})
		if err != nil {
			return pending("reserve upload file", err)
		}
		file, _ = res.First()
	}
	out.UploadFileID = file.ID
	ops, err := upload.ParseOperations(file)
	if err != nil {
		return pending("upload operations", err)
	}
	if err := upload.ValidateOperations(ops, size); err != nil {
		return pending("upload operations", err)
	}
	sum, err := upload.FileMD5(io.NewSectionReader(fh, 0, size))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in.File, err)
	}
	deps := upload.DepsFromEnv(env)
	if err := upload.PutParts(ctx, deps.Fetch, env.Clock, fh, size, ops, deps.Progress); err != nil {
		return pending("upload parts", err)
	}
	if _, err := env.ASC.Do(wctx, tool.Request{
		Method: http.MethodPatch, Path: "/v1/backgroundAssetUploadFiles/" + url.PathEscape(file.ID), Write: true,
		Body: map[string]any{"data": map[string]any{
			"type": "backgroundAssetUploadFiles", "id": file.ID,
			"attributes": map[string]any{
				"uploaded":            true,
				"sourceFileChecksums": map[string]any{"file": map[string]string{"hash": sum, "algorithm": "MD5"}},
			},
		}},
	}); err != nil {
		return pending("commit upload file "+file.ID, err)
	}
	out.UploadedBytes = size
	return finishPackUpload(ctx, env, app.ID, out, wait)
}

// packWait returns how long to poll; 0 means do not wait.
func packWait(env *tool.Env, in packUploadIn) time.Duration {
	if in.Wait != nil && !*in.Wait {
		return 0
	}
	wait := packWaitDefault
	if in.WaitSeconds > 0 {
		wait = min(time.Duration(in.WaitSeconds)*time.Second, packWaitMax)
	}
	if !env.HasProgress() {
		wait = min(wait, tool.NoProgressWaitCap)
	}
	return wait
}

// planPackUpload reads the pack, its newest version and that version's
// upload files and decides what an upload of name/size would do.
func planPackUpload(ctx context.Context, env *tool.Env, appID, pack, name string, size int64) (packPlan, error) {
	var p packPlan
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(appID)+"/backgroundAssets", tool.ListOptions{
		Query:    url.Values{"filter[assetPackIdentifier]": {pack}},
		Fields:   map[string][]string{"backgroundAssets": {"archived", "assetPackIdentifier"}},
		MaxItems: packsMax,
	})
	if err != nil {
		return p, err
	}
	for _, r := range res.Data {
		if r.Str("assetPackIdentifier") != pack {
			continue
		}
		if archived, _ := r.Bool("archived"); archived {
			return p, fmt.Errorf("%w: asset pack %s is archived; it takes no new versions", tool.ErrInvalidArgs, pack)
		}
		p.packID = r.ID
	}
	if p.packID == "" {
		return p, nil
	}
	versions, err := packVersions(ctx, env, p.packID)
	if err != nil {
		return p, fmt.Errorf("asset pack %s versions: %w", pack, err)
	}
	if len(versions) == 0 {
		return p, nil
	}
	newest := versions[0]
	if newest.State == packFailed {
		return p, nil
	}
	files, err := env.ASC.List(ctx, "/v1/backgroundAssetVersions/"+url.PathEscape(newest.ID)+"/backgroundAssetUploadFiles", tool.ListOptions{
		Fields:   map[string][]string{"backgroundAssetUploadFiles": {"assetDeliveryState", "assetType", "fileName", "fileSize"}},
		MaxItems: packFilesMax,
	})
	if err != nil {
		return p, fmt.Errorf("asset pack %s version %s upload files: %w", pack, newest.Version, err)
	}
	var open, sent string
	for _, f := range files.Data {
		n, _ := f.Int("fileSize")
		if f.Str("assetType") != "ASSET" || string(f.Text("fileName")) != name || int64(n) != size {
			continue
		}
		ds, _ := f.Attributes["assetDeliveryState"].(map[string]any)
		if s, _ := ds["state"].(string); s == "" || s == upload.StateAwaitingUpload {
			open = f.ID
		} else if s != upload.StateFailed {
			sent = f.ID
		}
	}
	switch newest.State {
	case packAwaitingUpload:
		p.version, p.reuse = newest, true
		if sent != "" {
			// Committed, Apple has not moved the version on yet.
			p.already, p.fileID = true, sent
		} else {
			p.fileID = open
		}
	default: // PROCESSING, COMPLETE
		id := sent
		if id == "" {
			id = open
		}
		if id != "" {
			p.version, p.already, p.fileID = newest, true, id
		}
	}
	return p, nil
}

// packGate previews without confirm; creating the pack reserves its
// identifier for good and needs a human, an upload to an existing pack needs
// confirm:true bound to the version and reservation it was previewed for.
func packGate(ctx context.Context, env *tool.Env, in packUploadIn, app tool.App, plan packPlan, name string, size int64) (context.Context, *tool.Result) {
	appLabel := fmt.Sprintf("%s (%s, %s)", tool.SanitizeLine(app.Name, 80), app.BundleID, app.ID)
	intended := map[string]any{"file": name, "size_bytes": size, "pack": "existing", "version": "new"}
	pv := tool.PlanPreview{Intended: intended, Warnings: []string{
		"the archive is not read: its manifest's assetPackID must equal " + in.Pack + ", or Apple fails the version",
	}}
	if plan.reuse {
		intended["version"] = "reuse " + plan.version.Version + " (" + packAwaitingUpload + ")"
		intended["upload_file"] = "new reservation"
		if plan.fileID != "" {
			intended["upload_file"] = "reuse reservation " + plan.fileID
		}
		pv.Affected = append(pv.Affected, "backgroundAssetVersions "+plan.version.ID)
	} else {
		pv.Affected = append(pv.Affected, "POST /v1/backgroundAssetVersions")
	}
	if plan.packID == "" {
		intended["pack"] = "create"
		pv.Summary = fmt.Sprintf("create asset pack %s for app %s and upload %s (%d bytes) as its first version", in.Pack, appLabel, name, size)
		pv.Affected = append([]string{"POST /v1/backgroundAssets " + in.Pack + " for app " + appLabel}, pv.Affected...)
		pv.Irreversible = []string{"asset pack identifier " + in.Pack + " is reserved for app " + appLabel + " for good: it cannot be renamed"}
		return tool.RequireHumanConfirm(ctx, env, in.Confirm, pv)
	}
	pv.Summary = fmt.Sprintf("upload %s (%d bytes) to asset pack %s of app %s", name, size, in.Pack, appLabel)
	target := map[string]any{"pack": plan.packID, "version": plan.version.ID, "file": plan.fileID, "size": size}
	return tool.RequireConfirmTarget(ctx, env, in.Confirm, target, pv)
}

// finishPackUpload polls the version until COMPLETE, FAILED or the timeout
// and shapes the result.
func finishPackUpload(ctx context.Context, env *tool.Env, appID string, out packUploadOut, wait time.Duration) (*tool.Result, error) {
	next := tool.NextStep{Tool: "asset_packs_list", Args: map[string]any{"app": appID}}
	hint := "the file is uploaded; Apple is still processing it. Call again with the same arguments, or check with asset_packs_list"
	deadline := env.Clock.Now().Add(wait)
	for {
		v, err := packVersion(ctx, env, out.Version.ID)
		if err != nil {
			if out.Already || wait == 0 {
				return tool.Fail(fmt.Errorf("asset pack version %s: %w", out.Version.ID, err)), nil
			}
			return tool.StillProcessing(out, hint+" (reading the version failed: "+tool.SanitizeLine(err.Error(), 200)+")").Then(next), nil
		}
		out.Version = &v
		switch v.State {
		case packComplete:
			return tool.OK(out).Then(next), nil
		case packFailed:
			out.Hint = "Apple rejected this version: fix the reported errors, rebuild the archive and upload again (a new version is created)"
			return tool.OK(out).Then(next), nil
		}
		remaining := deadline.Sub(env.Clock.Now())
		if remaining <= 0 {
			return tool.StillProcessing(out, hint).Then(next), nil
		}
		if err := env.Clock.Sleep(ctx, min(packPollInterval, remaining)); err != nil {
			return nil, err
		}
	}
}
