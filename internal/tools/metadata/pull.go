package metadata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/idoo/hamn/internal/metadata"
	"github.com/idoo/hamn/internal/tool"
)

type pullIn struct {
	App       tool.AppRef     `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Platform  tool.Platform   `json:"platform,omitempty" jsonschema:"default: inferred from the app"`
	Version   tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default, falls back to live), live or a version string"`
	Locales   tool.LocaleSet  `json:"locales,omitempty" jsonschema:"locales or aliases; default all"`
	ExportDir string          `json:"export_dir,omitempty" jsonschema:"write a fastlane deliver metadata dir here (new, empty or named metadata), inside the project"`
}

type pullOut struct {
	App       appOut                            `json:"app"`
	Platform  tool.Platform                     `json:"platform"`
	Version   versionOut                        `json:"version"`
	AppInfo   *appInfoOut                       `json:"app_info,omitempty"`
	Locales   map[string]*metadata.LocaleFields `json:"locales"`
	ExportDir string                            `json:"export_dir,omitempty"`
	Written   []string                          `json:"written,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "metadata_pull", Toolset: tool.Metadata, Class: tool.Read, Idempotent: true,
		Title: "Pull store listing",
		Description: "Every locale's listing (name, subtitle, privacy URLs; description, keywords, What's New, URLs) of the " +
			"editable version, else live. export_dir writes a fastlane metadata dir locally. Edit with metadata_plan.",
		Enums: map[string][]any{"platform": platformEnum},
	}, metadataPull))
}

func metadataPull(ctx context.Context, env *tool.Env, in pullIn) (*tool.Result, error) {
	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, true)
	if err != nil {
		return nil, err
	}
	editable, live, _, err := appInfos(ctx, env, t.App.ID, "")
	if err != nil {
		return nil, err
	}
	info := editable
	if info == nil || t.Version.State == versionStateLive {
		if live != nil {
			info = live
		}
	}
	infoID := ""
	out := pullOut{App: t.appOut(), Platform: t.Platform, Version: t.versionOut()}
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
			}
		}
		listing.Locales = kept
	}
	out.Locales = listing.Locales

	res := tool.OK(&out)
	if in.ExportDir != "" {
		if err := checkExportDir(env.Files, in.ExportDir); err != nil {
			return nil, err
		}
		written, err := metadata.ToFastlane(env.Files, in.ExportDir, &metadata.Listing{Locales: listing.Locales})
		out.ExportDir = in.ExportDir
		out.Written = written
		if err != nil {
			return nil, err
		}
		res.Warn("wrote local files only; nothing changed in App Store Connect")
	}
	return res.Then(tool.NextStep{Tool: "metadata_plan", Args: map[string]any{"app": t.App.ID, "version": t.Version.String}}), nil
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
