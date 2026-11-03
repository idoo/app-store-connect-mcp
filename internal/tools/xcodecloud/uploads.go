package xcodecloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// ciPlatforms maps Xcode Cloud workflow action platforms to the platforms
// of build uploads (watchOS apps ship inside the iOS upload).
var ciPlatforms = map[string]tool.Platform{
	"IOS": tool.IOS, "WATCHOS": tool.IOS, "MACOS": tool.MacOS, "TVOS": tool.TVOS, "VISIONOS": tool.VisionOS,
}

// attachUploads reads, for a run with a completed ARCHIVE action, the
// app's build uploads of the run's build number (Xcode Cloud uses the run
// number as CFBundleVersion) and attaches each archive action's platform
// uploads; uploads no archive action claims go to out.OtherUploads.
// Apple's errors on a failed upload ("Preparing build for App Store
// Connect failed") are only there. Runs without one cost no call. It
// returns warnings; a read failure is one, never the status call's error.
func attachUploads(ctx context.Context, env *tool.Env, out *buildStatusOut) []string {
	var archives []int
	for i, a := range out.Actions {
		if a.Type == "ARCHIVE" && a.ExecutionProgress == "COMPLETE" {
			archives = append(archives, i)
		}
	}
	if len(archives) == 0 || out.Run.Number <= 0 {
		return nil
	}
	platforms, appID, err := runArchivePlatforms(ctx, env, out.Run.ID)
	var uploads []tool.BuildUpload
	if err == nil {
		uploads, err = tool.ListBuildUploads(ctx, env, appID, url.Values{
			"filter[cfBundleVersion]": {strconv.Itoa(out.Run.Number)},
			"limit":                   {"50"},
		})
	}
	if err != nil {
		return []string{fmt.Sprintf("could not read the App Store Connect uploads of build %d: %s", out.Run.Number, tool.SanitizeText(err.Error()))}
	}
	var warnings []string
	claimed := make([]bool, len(uploads))
	for _, i := range archives {
		a := &out.Actions[i]
		p, ok := platforms[string(a.Name)] // trusted: matched against the workflow's own action names
		if !ok && len(archives) > 1 {
			continue
		}
		var failed []string
		for j, u := range uploads {
			if ok && u.Platform != p {
				continue
			}
			claimed[j] = true
			a.Uploads = append(a.Uploads, u)
			if u.State == "FAILED" {
				failed = append(failed, issueCodes(u))
			}
		}
		if len(failed) > 0 && a.CompletionStatus != "SUCCEEDED" {
			warnings = append(warnings, fmt.Sprintf("App Store Connect rejected the %s upload of build %d (%s); Apple's reasons are in actions[].uploads[].errors",
				uploadPlatform(a.Uploads), out.Run.Number, strings.Join(failed, "; ")))
		}
	}
	for j, u := range uploads {
		if !claimed[j] {
			out.OtherUploads = append(out.OtherUploads, u)
		}
	}
	return warnings
}

// runArchivePlatforms returns the upload platform of each workflow action
// by name and the run's app id (two reads: the run with its workflow, the
// product's app).
func runArchivePlatforms(ctx context.Context, env *tool.Env, runID string) (map[string]tool.Platform, string, error) {
	resp, err := env.ASC.Get(ctx, "/v1/ciBuildRuns/"+url.PathEscape(runID), url.Values{
		"fields[ciBuildRuns]": {"product,workflow"},
		"include":             {"product,workflow"},
		"fields[ciWorkflows]": {"actions"},
		"fields[ciProducts]":  {"name"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("read build run %s: %w", runID, err)
	}
	run, ok := resp.First()
	if !ok || run.RelID("product") == "" {
		return nil, "", fmt.Errorf("build run %s has no Xcode Cloud product", runID)
	}
	platforms := map[string]tool.Platform{}
	if wf, ok := tool.IndexIncluded(resp.Included)["ciWorkflows/"+run.RelID("workflow")]; ok {
		list, _ := wf.Attributes["actions"].([]any)
		for _, v := range list {
			m, _ := tool.Unwrap(v).(map[string]any)
			name, _ := m["name"].(string)
			plat, _ := m["platform"].(string)
			if p, ok := ciPlatforms[plat]; ok && name != "" {
				platforms[name] = p
			}
		}
	}
	app, err := env.ASC.Get(ctx, "/v1/ciProducts/"+url.PathEscape(run.RelID("product"))+"/app", url.Values{"fields[apps]": {"bundleId"}})
	if tool.APIStatus(err) == http.StatusNotFound {
		return nil, "", fmt.Errorf("the run's Xcode Cloud product has no app")
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the app of the run's Xcode Cloud product: %w", err)
	}
	a, ok := app.First()
	if !ok {
		return nil, "", fmt.Errorf("the run's Xcode Cloud product has no app")
	}
	return platforms, a.ID, nil
}

// issueCodes renders a failed upload's error codes, e.g. "90626, 90626".
func issueCodes(u tool.BuildUpload) string {
	codes := make([]string, 0, len(u.Errors))
	for _, e := range u.Errors {
		if e.Code != "" {
			codes = append(codes, e.Code)
		}
	}
	if len(codes) == 0 {
		return "no error code"
	}
	return strings.Join(codes, ", ")
}

// uploadPlatform names the platform of an action's uploads.
func uploadPlatform(us []tool.BuildUpload) string {
	if len(us) > 0 && us[0].Platform != "" {
		return string(us[0].Platform)
	}
	return "archive"
}
