package metadata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/metadata"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type pullIn struct {
	App       tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform  tool.Platform   `json:"platform,omitempty"`
	Version   tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default, else live), live or a version string"`
	Locales   tool.LocaleSet  `json:"locales,omitempty" jsonschema:"locales or aliases; default all"`
	ExportDir string          `json:"export_dir,omitempty" jsonschema:"fastlane dir; never overwrites edited files"`
	Fields    []string        `json:"fields,omitempty" jsonschema:"only these fields, e.g. [\"subtitle\",\"keywords\"]; default all"`
}

type pullOut struct {
	App      appOut        `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionOut    `json:"version"`
	// Source is "editable" or "live"; Reason explains a live fallback.
	Source           string                            `json:"source"`
	Reason           string                            `json:"reason,omitempty"`
	InFlight         *versionOut                       `json:"in_flight,omitempty"`
	AppInfo          *appInfoOut                       `json:"app_info,omitempty"`
	Locales          map[string]*metadata.LocaleFields `json:"locales"`
	MissingLocales   []string                          `json:"missing_locales,omitempty"`
	AvailableLocales []string                          `json:"available_locales,omitempty"`
	ExportDir        string                            `json:"export_dir,omitempty"`
	Written          []string                          `json:"written,omitempty"`
	Conflicts        []string                          `json:"conflicts,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "metadata_pull", Toolset: tool.Metadata, Class: tool.Read, Idempotent: true,
		Title: "Pull store listing",
		Description: "Every locale's listing (app info and version text) of the editable version, else live. export_dir " +
			"writes a fastlane metadata dir. Edit with metadata_plan.",
		Enums: map[string][]any{"platform": platformEnum, "fields": fieldEnum},
	}, metadataPull))
}

// fieldEnum lists the listing field keys accepted by metadata_pull fields.
var fieldEnum = func() []any {
	out := make([]any, 0, len(metadata.Fields))
	for _, f := range metadata.Fields {
		out = append(out, f.Key)
	}
	return out
}()

// onlyFields returns a copy of lf holding just the listed field keys.
func onlyFields(lf *metadata.LocaleFields, keys []string) *metadata.LocaleFields {
	out := &metadata.LocaleFields{}
	for _, k := range keys {
		if v, ok := lf.Get(k); ok {
			_ = out.Set(k, v) // keys are schema-validated field names
		}
	}
	return out
}

func metadataPull(ctx context.Context, env *tool.Env, in pullIn) (*tool.Result, error) {
	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, true)
	if err != nil {
		return nil, err
	}
	infos, err := appInfos(ctx, env, t.App.ID, "")
	if err != nil {
		return nil, err
	}
	info := infos.Editable
	if info == nil {
		info = infos.Locked
	}
	if (info == nil || t.Version.State == versionStateLive) && infos.Live != nil {
		info = infos.Live
	}
	infoID := ""
	out := pullOut{App: t.appOut(), Platform: t.Platform, Version: t.versionOut(), Source: "editable"}
	switch {
	case t.Version.State == versionStateLive:
		out.Source = "live"
	case slices.Contains(inFlightLocked, t.Version.State):
		out.Source = "in_flight"
	}
	var versions []activeVersion
	var versionsErr error
	if in.Platform == "" && len(t.App.Platforms) > 1 {
		versions, versionsErr = activeVersions(ctx, env, t.App.ID)
	}
	if t.FellBack {
		if versions != nil && versionsErr == nil {
			out.InFlight = inFlightOf(versions, t.Platform)
		} else if out.InFlight, err = inFlightVersion(ctx, env, t.App.ID, t.Platform); err != nil {
			return nil, err
		}
		out.Reason = "no editable version"
		if out.InFlight != nil {
			out.Reason = fmt.Sprintf("version %s is %s and not editable", out.InFlight.Version, out.InFlight.State)
		}
	}
	if info != nil {
		infoID = info.ID
		out.AppInfo = &appInfoOut{ID: info.ID, State: info.Str("state")}
	}
	listing, err := metadata.Pull(ctx, env.ASC, t.Version.ID, infoID)
	if err != nil {
		return nil, err
	}
	if len(in.Locales) > 0 && !in.Locales.IsAll() {
		want, err := env.Resolve.Locales(ctx, t.App.ID, in.Locales)
		if err != nil {
			return nil, err
		}
		kept := make(map[string]*metadata.LocaleFields, len(want))
		for _, code := range want {
			if lf, ok := listing.Locales[code]; ok {
				kept[code] = lf
			} else {
				out.MissingLocales = append(out.MissingLocales, code)
			}
		}
		if len(out.MissingLocales) > 0 {
			out.AvailableLocales = slices.Sorted(maps.Keys(listing.Locales))
		}
		listing.Locales = kept
	}
	if len(in.Fields) > 0 {
		for code, lf := range listing.Locales {
			listing.Locales[code] = onlyFields(lf, in.Fields)
		}
	}
	out.Locales = listing.Locales

	res := tool.OK(&out)
	if versionsErr != nil {
		res.Warn("other platforms not checked: " + versionsErr.Error())
	}
	for _, n := range otherPlatformNotes(versions, t.Platform) {
		res.Warn(n)
	}
	if len(out.MissingLocales) > 0 {
		res.Warn(fmt.Sprintf("version %s has no listing for %s (available: %s); add one via metadata_plan with listing.locales.<locale>",
			t.Version.String, strings.Join(out.MissingLocales, ", "), strings.Join(out.AvailableLocales, ", ")))
	}
	if out.Reason != "" {
		res.Warn("showing the live version " + t.Version.String + ": " + out.Reason)
	}
	if in.ExportDir != "" {
		if err := checkExportDir(env.Files, in.ExportDir); err != nil {
			return nil, err
		}
		written, conflicts, err := metadata.ToFastlane(env.Files, in.ExportDir, &metadata.Listing{Locales: listing.Locales})
		out.ExportDir = in.ExportDir
		out.Written, out.Conflicts = written, conflicts
		if err != nil {
			return nil, err
		}
		res.Warn("wrote local files only; nothing changed in App Store Connect")
		if len(conflicts) > 0 {
			res.Warn(fmt.Sprintf("%d local file(s) differ from App Store Connect and were kept; delete them or use a new export_dir to take the remote text", len(conflicts)))
		}
	}
	if out.InFlight != nil || out.Source == "in_flight" {
		return res.Then(tool.NextStep{Tool: "release_review_status", Args: map[string]any{"app": t.App.ID, "platform": string(t.Platform)}}), nil
	}
	if t.FellBack {
		return res.Then(tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": t.App.ID, "platform": string(t.Platform)}}), nil
	}
	return res.Then(tool.NextStep{Tool: "metadata_plan", Args: map[string]any{"app": t.App.ID, "version": t.Version.String}}), nil
}

// inFlightLocked are submitted-but-unreleased version states (not editable).
var inFlightLocked = []string{"WAITING_FOR_REVIEW", "IN_REVIEW", "ACCEPTED", "PENDING_DEVELOPER_RELEASE", "PENDING_APPLE_RELEASE", "PROCESSING_FOR_DISTRIBUTION"}

// inFlightVersion returns the app's newest submitted-but-unreleased version
// of platform, or nil.
func inFlightVersion(ctx context.Context, env *tool.Env, appID string, platform tool.Platform) (*versionOut, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appStoreVersions", url.Values{
		"filter[platform]":         {string(platform)},
		"filter[appVersionState]":  {strings.Join(inFlightLocked, ",")},
		"fields[appStoreVersions]": {"versionString,appVersionState"},
		"limit":                    {"1"},
	})
	if err != nil {
		return nil, fmt.Errorf("list in-flight versions: %w", err)
	}
	v, ok := resp.First()
	if !ok {
		return nil, nil
	}
	return &versionOut{ID: v.ID, Version: v.Str("versionString"), State: v.Str("appVersionState")}, nil
}

// inFlightOf returns the first submitted-but-unreleased version of platform
// in vs, or nil.
func inFlightOf(vs []activeVersion, platform tool.Platform) *versionOut {
	for _, v := range vs {
		if v.Platform == string(platform) && slices.Contains(inFlightLocked, v.State) {
			return &versionOut{ID: v.ID, Version: v.Version, State: v.State}
		}
	}
	return nil
}

// checkExportDir refuses to write into an existing non-empty directory unless
// it is a fastlane "metadata" directory.
func checkExportDir(files tool.Files, dir string) error {
	fi, err := files.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%w: export_dir: %v", tool.ErrInvalidArgs, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: export_dir %s is a file", tool.ErrInvalidArgs, dir)
	case filepath.Base(filepath.Clean(dir)) == "metadata":
		return nil
	}
	root, err := files.Resolve(dir)
	if err != nil {
		return fmt.Errorf("%w: export_dir: %v", tool.ErrInvalidArgs, err)
	}
	empty := true
	err = files.WalkDir(root, func(p string, _ fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p != root {
			empty = false
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: export_dir: %v", tool.ErrInvalidArgs, err)
	}
	if !empty {
		return fmt.Errorf("%w: export_dir %s is not empty; use a new directory or one named metadata", tool.ErrInvalidArgs, dir)
	}
	return nil
}
