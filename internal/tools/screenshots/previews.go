package screenshots

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/upload"
)

// previewMIME maps accepted app preview video extensions to MIME types.
var previewMIME = map[string]string{
	".mov": "video/quicktime",
	".mp4": "video/mp4",
	".m4v": "video/x-m4v",
}

type previewIn struct {
	App                  tool.AppRef     `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Platform             tool.Platform   `json:"platform,omitempty" jsonschema:"default: inferred from the app"`
	Version              tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default) or a version string of an editable version"`
	Locale               string          `json:"locale" jsonschema:"locale of the listing, e.g. en-US"`
	File                 string          `json:"file" jsonschema:".mov, .mp4 or .m4v video inside the project"`
	PreviewType          string          `json:"preview_type,omitempty" jsonschema:"preview slot (IPHONE_67, DESKTOP …) or a display such as 6.9, ipad-13, mac; default: main slot of the platform"`
	PreviewFrameTimeCode string          `json:"preview_frame_time_code,omitempty" jsonschema:"poster frame as HH:MM:SS:FF, e.g. 00:00:05:00"`
	Wait                 int             `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for processing (default 45, max 300)"`
}

type previewOut struct {
	App         appOut              `json:"app"`
	Platform    tool.Platform       `json:"platform"`
	Version     versionOut          `json:"version"`
	Locale      string              `json:"locale"`
	PreviewType string              `json:"preview_type"`
	SetID       string              `json:"set_id"`
	SetCreated  bool                `json:"set_created,omitempty"`
	File        string              `json:"file"`
	Preview     *upload.AssetResult `json:"preview,omitempty"`
	Existing    *remotePreview      `json:"existing,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "previews_upload", Toolset: tool.Screenshots, Class: tool.Write,
		Title: "Upload an app preview video",
		Description: "Uploads one app preview video to a locale's preview set of the editable version, creating the set if " +
			"needed; optional poster frame time code. Same checksum already present = no-op. Max 3 previews per set.",
		Enums: map[string][]any{"platform": platformEnum},
	}, previewsUpload))
}

func previewsUpload(ctx context.Context, env *tool.Env, in previewIn) (*tool.Result, error) {
	mime, ok := previewMIME[strings.ToLower(filepath.Ext(in.File))]
	if !ok {
		return nil, fmt.Errorf("%w: preview must be a .mov, .mp4 or .m4v file", tool.ErrInvalidArgs)
	}
	locale, err := applemeta.NormalizeLocale(in.Locale)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
	}
	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, true)
	if err != nil {
		return nil, err
	}
	pvType, err := parsePreviewType(in.PreviewType, t.Platform)
	if err != nil {
		return nil, err
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
	sum, err := upload.FileMD5(fh)
	if err != nil {
		return nil, fmt.Errorf("checksum %s: %w", in.File, err)
	}

	locs, err := localizations(ctx, env, t.Version.ID)
	if err != nil {
		return nil, err
	}
	loc, ok := locs[locale]
	if !ok {
		return nil, tool.WithNext(fmt.Errorf("%s %s: %w", t.Version.String, locale, errMissingLocalization),
			tool.NextStep{Tool: "metadata_plan", Args: map[string]any{"app": t.App.ID, "platform": string(t.Platform)}})
	}
	out := previewOut{
		App: t.appOut(), Platform: t.Platform, Version: t.versionOut(), Locale: locale,
		PreviewType: pvType, File: in.File,
	}
	sets, err := previewSets(ctx, env, loc.ID, pvType)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(sets, func(s remotePreviewSet) bool { return s.PreviewType == pvType })
	if i >= 0 {
		set := sets[i]
		out.SetID = set.ID
		if j := slices.IndexFunc(set.Previews, func(p remotePreview) bool { return p.Checksum == sum }); j >= 0 {
			out.Existing = &set.Previews[j]
			return tool.Already(out), nil
		}
		if len(set.Previews) >= maxPreviewsPerSet {
			return nil, tool.WithNext(fmt.Errorf("%w: %s %s already has %d previews (the maximum); delete one first", tool.ErrInvalidArgs, locale, pvType, len(set.Previews)),
				tool.NextStep{Tool: "screenshots_list", Args: map[string]any{"app": t.App.ID, "locales": []string{locale}}})
		}
	} else {
		resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/appPreviewSets", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":       "appPreviewSets",
				"attributes": map[string]any{"previewType": pvType},
				"relationships": map[string]any{"appStoreVersionLocalization": map[string]any{
					"data": map[string]string{"type": "appStoreVersionLocalizations", "id": loc.ID},
				}},
			},
		}})
		if err != nil {
			return nil, fmt.Errorf("create %s preview set for %s: %w", pvType, locale, err)
		}
		r, ok := resp.First()
		if !ok || r.ID == "" {
			return nil, errors.New("create preview set: response has no resource")
		}
		out.SetID, out.SetCreated = r.ID, true
	}

	wait := in.Wait
	switch {
	case wait <= 0:
		wait = defaultWait
	case wait > maxWait:
		wait = maxWait
	}
	attrs := map[string]any{"mimeType": mime}
	if in.PreviewFrameTimeCode != "" {
		attrs["previewFrameTimeCode"] = in.PreviewFrameTimeCode
	}
	res, err := upload.Asset(ctx, upload.DepsFromEnv(env), upload.AssetSpec{
		CreatePath: "/v1/appPreviews", Type: "appPreviews",
		Parent:   upload.Relationship{Name: "appPreviewSet", Type: "appPreviewSets", ID: out.SetID},
		FileName: filepath.Base(in.File), FileSize: st.Size(), File: fh, Attributes: attrs,
		Poll: upload.PollOptions{MaxWait: time.Duration(wait) * time.Second}, StateField: "videoDeliveryState",
	})
	out.Preview = res
	if err != nil {
		if res != nil {
			return tool.Fail(err), nil
		}
		return nil, err
	}
	if res.StillProcessing {
		return tool.StillProcessing(out, "Apple is still processing the video; check it later with screenshots_list").
			Then(tool.NextStep{Tool: "screenshots_list", Args: map[string]any{"app": t.App.ID, "locales": []string{locale}}}), nil
	}
	return tool.OK(out), nil
}

// parsePreviewType accepts a PreviewType enum value or a display name; empty
// means the platform's main slot.
func parsePreviewType(s string, platform tool.Platform) (string, error) {
	if s == "" {
		switch platform {
		case tool.MacOS:
			return "DESKTOP", nil
		case tool.TVOS:
			return "APPLE_TV", nil
		case tool.VisionOS:
			return "APPLE_VISION_PRO", nil
		default:
			return "IPHONE_67", nil
		}
	}
	up := strings.ToUpper(strings.TrimSpace(s))
	if slices.Contains(applemeta.PreviewTypes(), applemeta.PreviewType(up)) {
		return up, nil
	}
	d, err := applemeta.ParseDisplay(s)
	if err == nil {
		if spec, ok := applemeta.LookupDisplay(d); ok && spec.Preview != "" && !spec.IMessage {
			return string(spec.Preview), nil
		}
	}
	return "", fmt.Errorf("%w: unknown preview type %q; use a slot such as IPHONE_67, IPAD_PRO_3GEN_129, DESKTOP or a display such as 6.9", tool.ErrInvalidArgs, s)
}
