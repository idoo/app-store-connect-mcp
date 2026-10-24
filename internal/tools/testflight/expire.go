package testflight

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxExpireBuilds caps the builds one builds_expire call handles.
const maxExpireBuilds = 50

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "builds_expire", Toolset: tool.TestFlight, Class: tool.Destructive,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Expire builds",
		Description: "Expire up to 50 builds so testers can no longer install them. Irreversible. Without confirm:true " +
			"previews; already expired builds are skipped.",
		Enums: map[string][]any{"platform": platformEnum},
	}, buildsExpire))
}

type expireIn struct {
	tool.ConfirmFlag
	App      tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform   `json:"platform,omitempty"`
	Builds   []tool.BuildRef `json:"builds" jsonschema:"1-50 build numbers, ids, latest or latest-valid"`
}

type expireBuildOut struct {
	buildOut
	Platform string          `json:"platform,omitempty"`
	Uploaded string          `json:"uploaded,omitempty"`
	Outcome  string          `json:"outcome,omitempty"` // expired|already|error|unknown
	Error    *tool.ErrorBody `json:"error,omitempty"`
}

func (b expireBuildOut) label() string {
	s := "build " + b.Number
	if b.Version != "" {
		s += " (" + b.Version + ")"
	}
	return s + " id " + b.ID
}

type expireOut struct {
	App     appRef           `json:"app"`
	Builds  []expireBuildOut `json:"builds"`
	Expired int              `json:"expired"`
	Already int              `json:"already"`
	Failed  int              `json:"failed,omitempty"`
}

func buildsExpire(ctx context.Context, env *tool.Env, in expireIn) (*tool.Result, error) {
	if len(in.Builds) == 0 || len(in.Builds) > maxExpireBuilds {
		return nil, tool.InvalidArgs("builds: pass 1-%d builds", maxExpireBuilds)
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	var ids []string
	seen := map[string]bool{}
	for _, r := range in.Builds {
		b, err := env.Resolve.Build(ctx, app.ID, platform, r)
		if err != nil {
			return nil, err
		}
		if !seen[b.ID] {
			seen[b.ID] = true
			ids = append(ids, b.ID)
		}
	}
	list, err := env.ASC.List(ctx, "/v1/builds", tool.ListOptions{
		Query:    url.Values{"filter[id]": {strings.Join(ids, ",")}, "filter[app]": {app.ID}},
		Fields:   map[string][]string{"builds": {"version", "expired", "processingState", "uploadedDate", "preReleaseVersion"}, "preReleaseVersions": {"version", "platform"}},
		Include:  []string{"preReleaseVersion"},
		Limit:    maxExpireBuilds,
		MaxItems: maxExpireBuilds,
	})
	if err != nil {
		return nil, fmt.Errorf("read builds: %w", err)
	}
	inc := includedByID(list.Included)
	byID := make(map[string]tool.Resource, len(list.Data))
	for _, r := range list.Data {
		byID[r.ID] = r
	}
	out := &expireOut{App: appOut(app)}
	var todo []int
	var affected []string
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			return nil, tool.InvalidArgs("build %s does not belong to app %s", id, app.Name)
		}
		pre := inc["preReleaseVersions/"+r.RelID("preReleaseVersion")]
		b := expireBuildOut{
			buildOut: buildOut{ID: id, Number: r.Str("version"), Version: pre.Str("version"), ProcessingState: r.Str("processingState")},
			Platform: pre.Str("platform"), Uploaded: r.Str("uploadedDate"),
		}
		if exp, _ := r.Bool("expired"); exp {
			b.Outcome = outcomeAlready
			out.Already++
		} else {
			todo = append(todo, len(out.Builds))
			affected = append(affected, b.label())
		}
		out.Builds = append(out.Builds, b)
	}
	if len(todo) == 0 {
		return tool.Already(out), nil
	}

	targets := make([]string, 0, len(todo))
	for _, i := range todo {
		targets = append(targets, out.Builds[i].ID)
	}
	// Refs like "latest" can resolve to another build by the time confirm
	// arrives; bind the previewed build ids so confirm expires exactly them.
	wctx, res := tool.RequireConfirmTarget(ctx, env, in.Confirm, targets, tool.PlanPreview{
		Summary:  fmt.Sprintf("expire %d build(s) of %q: %s", len(todo), app.Name, strings.Join(affected, ", ")),
		Current:  out.Builds,
		Intended: "expired:true on " + strconv.Itoa(len(todo)) + " build(s)",
		Affected: affected,
		Irreversible: []string{
			"expired builds can no longer be installed by testers and cannot be un-expired",
			"an expired build cannot be submitted to the App Store",
		},
	})
	if res != nil {
		return res, nil
	}
	var errs []error
	for _, i := range todo {
		b := &out.Builds[i]
		_, err := write(wctx, env, http.MethodPatch, "/v1/builds/"+b.ID, patchBody("builds", b.ID, map[string]any{"expired": true}))
		if err != nil {
			b.Outcome, b.Error = itemError(err)
			out.Failed++
			errs = append(errs, err)
			continue
		}
		b.Outcome = "expired"
		out.Expired++
	}
	return tool.Batch(out, out.Expired, out.Already, errs), nil
}
