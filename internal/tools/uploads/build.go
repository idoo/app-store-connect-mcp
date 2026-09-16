package uploads

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/upload"
)

// Wait bounds for Apple's processing after the upload is committed.
const (
	defaultWaitSeconds = 45
	maxWaitSeconds     = 300
)

type buildUploadIn struct {
	App         tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform    tool.Platform `json:"platform,omitempty" jsonschema:"default: IOS for .ipa, MAC_OS for .pkg"`
	Path        string        `json:"path" jsonschema:".ipa or .pkg file inside the project"`
	Version     string        `json:"version,omitempty" jsonschema:"CFBundleShortVersionString; required for .pkg, read from Info.plist for .ipa"`
	BuildNumber string        `json:"build_number,omitempty" jsonschema:"CFBundleVersion; required for .pkg, read from Info.plist for .ipa"`
	Wait        int           `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for Apple's processing (default 45, max 300)"`
}

type buildUploadOut struct {
	App           tool.App `json:"app"`
	File          string   `json:"file"`
	SizeBytes     int64    `json:"size_bytes"`
	UploadedBytes int64    `json:"uploaded_bytes"`
	*upload.BuildResult
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "builds_upload", Toolset: tool.Uploads, Class: tool.Write,
		Title: "Upload a build (.ipa/.pkg)",
		Description: "Uploads an .ipa or .pkg via the BuildUpload API; no Xcode or Transporter needed. Version/build come from the IPA's " +
			"Info.plist (required for .pkg). Interrupted? Call again with the same file to resume. Apple processing takes minutes: follow with builds_wait.",
		Enums: map[string][]any{"platform": {string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}},
	}, buildsUpload))
}

func buildsUpload(ctx context.Context, env *tool.Env, in buildUploadIn) (*tool.Result, error) {
	if in.Path == "" {
		return nil, fmt.Errorf("%w: path is required", tool.ErrInvalidArgs)
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	fh, err := env.Files.Open(in.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: file: %v", tool.ErrInvalidArgs, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", in.Path, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", tool.ErrInvalidArgs, in.Path)
	}
	wait := in.Wait
	switch {
	case wait <= 0:
		wait = defaultWaitSeconds
	case wait > maxWaitSeconds:
		wait = maxWaitSeconds
	}

	res, err := upload.Build(ctx, upload.DepsFromEnv(env), upload.BuildSpec{
		AppID: app.ID, Platform: in.Platform,
		FileName: filepath.Base(in.Path), File: fh, FileSize: st.Size(),
		Version: in.Version, BuildNumber: in.BuildNumber,
		Poll: upload.PollOptions{MaxWait: time.Duration(wait) * time.Second},
	})
	listNext := func(platform tool.Platform, version string) tool.NextStep {
		args := map[string]any{"app": app.ID}
		if platform != "" {
			args["platform"] = string(platform)
		}
		if version != "" {
			args["version"] = version
		}
		return tool.NextStep{Tool: "builds_list", Args: args}
	}
	if err != nil {
		if errors.Is(err, upload.ErrDuplicateBuild) {
			return nil, tool.WithNext(err, listNext(in.Platform, in.Version))
		}
		if res != nil { // Apple finished processing with FAILED.
			return nil, tool.WithNext(err, listNext(res.Platform, res.Version))
		}
		return nil, err
	}

	out := buildUploadOut{App: app, File: in.Path, SizeBytes: st.Size(), UploadedBytes: st.Size(), BuildResult: res}
	if res.StillProcessing {
		return tool.StillProcessing(out, "the file is uploaded; Apple is still processing it. Wait with builds_wait (the build appears a few minutes after COMPLETE)").
			Then(tool.NextStep{Tool: "builds_wait", Args: map[string]any{
				"app": app.ID, "platform": string(res.Platform), "build": res.BuildNumber,
			}}), nil
	}
	return tool.OK(out).Then(tool.NextStep{Tool: "builds_wait", Args: map[string]any{
		"app": app.ID, "platform": string(res.Platform), "build": res.BuildNumber,
	}}), nil
}
