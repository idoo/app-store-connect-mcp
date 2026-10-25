package release

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

type buildsListIn struct {
	App            tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform       tool.Platform `json:"platform,omitempty"`
	Version        string        `json:"version,omitempty" jsonschema:"marketing version, e.g. 1.4"`
	State          string        `json:"state,omitempty" jsonschema:"processing state filter"`
	IncludeExpired bool          `json:"include_expired,omitempty" jsonschema:"also list expired builds"`
	Limit          int           `json:"limit,omitempty" jsonschema:"newest first, 1-200, default 20"`
}

type buildsWaitIn struct {
	App            tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform       tool.Platform `json:"platform,omitempty"`
	Build          tool.BuildRef `json:"build,omitempty" jsonschema:"latest (default), build number or build id"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty" jsonschema:"how long to wait (default 45, max 300; 50 when the client sends no progress token)"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "builds_list", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
		Title: "List builds",
		Description: "Builds newest first: number, version, processing state, dates, export compliance, TestFlight state " +
			"and groups. Expired hidden unless include_expired.",
		Enums: map[string][]any{"platform": platformEnum, "state": {"PROCESSING", "VALID", "INVALID", "FAILED"}},
	}, buildsList))
	tool.Register(tool.New(tool.Meta{
		Name: "builds_wait", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
		Title: "Wait for build processing",
		Description: "Poll a build until processing ends (VALID, INVALID or FAILED) with progress. Waits at most 50 s " +
			"unless the client sends a progress token. At the timeout returns still_processing; call again with the same args.",
		Enums: map[string][]any{"platform": platformEnum},
	}, buildsWait))
}

// buildRow is one build in builds_list.
type buildRow struct {
	ID                      string   `json:"id"`
	Number                  string   `json:"number"`
	Version                 string   `json:"version,omitempty"`
	Platform                string   `json:"platform,omitempty"`
	ProcessingState         string   `json:"processing_state"`
	Uploaded                string   `json:"uploaded,omitempty"`
	Expires                 string   `json:"expires,omitempty"`
	Expired                 bool     `json:"expired,omitempty"`
	UsesNonExemptEncryption *bool    `json:"uses_non_exempt_encryption,omitempty"`
	InternalState           string   `json:"internal_state,omitempty"`
	ExternalState           string   `json:"external_state,omitempty"`
	GroupCount              int      `json:"group_count"`
	Groups                  []string `json:"groups,omitempty"`
}

type buildsListOut struct {
	App      tool.App      `json:"app"`
	Platform tool.Platform `json:"platform"`
	Total    int           `json:"total,omitempty"`
	Returned int           `json:"returned"`
	Builds   []buildRow    `json:"builds"`
}

func buildsList(ctx context.Context, env *tool.Env, in buildsListIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 200)
	q := url.Values{
		"filter[app]":                        {app.ID},
		"filter[preReleaseVersion.platform]": {string(platform)},
		"sort":                               {"-uploadedDate"},
		"limit":                              {strconv.Itoa(limit)},
		"include":                            {"preReleaseVersion,buildBetaDetail,betaGroups"},
		"fields[builds]":                     {"version,uploadedDate,expirationDate,expired,processingState,usesNonExemptEncryption,preReleaseVersion,buildBetaDetail,betaGroups"},
		"fields[preReleaseVersions]":         {"version,platform"},
		"fields[buildBetaDetails]":           {"internalBuildState,externalBuildState"},
		"fields[betaGroups]":                 {"name,isInternalGroup"},
		"limit[betaGroups]":                  {"50"},
	}
	if in.Version != "" {
		q.Set("filter[preReleaseVersion.version]", in.Version)
	}
	if in.State != "" {
		q.Set("filter[processingState]", in.State)
	}
	if !in.IncludeExpired {
		q.Set("filter[expired]", "false")
	}
	resp, err := env.ASC.Get(ctx, "/v1/builds", q)
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	inc := map[string]tool.Resource{}
	for _, r := range resp.Included {
		inc[r.Type+":"+r.ID] = r
	}
	out := buildsListOut{App: app, Platform: platform, Builds: make([]buildRow, 0, len(resp.Data)), Total: pagingTotal(resp.Meta)}
	var processing *buildRow
	for _, b := range resp.Data {
		row := buildRow{
			ID: b.ID, Number: b.Str("version"), ProcessingState: b.Str("processingState"),
			Uploaded: b.Str("uploadedDate"), Expires: b.Str("expirationDate"),
		}
		row.Expired, _ = b.Bool("expired")
		if v, ok := b.Bool("usesNonExemptEncryption"); ok {
			row.UsesNonExemptEncryption = &v
		}
		if pv, ok := inc["preReleaseVersions:"+b.RelID("preReleaseVersion")]; ok {
			row.Version, row.Platform = pv.Str("version"), pv.Str("platform")
		}
		if bd, ok := inc["buildBetaDetails:"+b.RelID("buildBetaDetail")]; ok {
			row.InternalState, row.ExternalState = bd.Str("internalBuildState"), bd.Str("externalBuildState")
		}
		for _, id := range b.RelIDs("betaGroups") {
			row.GroupCount++
			if g, ok := inc["betaGroups:"+id]; ok {
				row.Groups = append(row.Groups, g.Str("name"))
			}
		}
		out.Builds = append(out.Builds, row)
		if processing == nil && row.ProcessingState == "PROCESSING" {
			processing = &out.Builds[len(out.Builds)-1]
		}
	}
	out.Returned = len(out.Builds)
	res := tool.OK(out)
	if processing != nil {
		res.Then(tool.NextStep{Tool: "builds_wait", Args: map[string]any{"app": app.ID, "platform": string(platform), "build": processing.ID}})
	}
	return res, nil
}

// pagingTotal reads meta.paging.total (0 when absent).
func pagingTotal(meta map[string]any) int {
	p, _ := meta["paging"].(map[string]any)
	n, _ := p["total"].(float64)
	return int(n)
}

// Build wait bounds.
const (
	waitDefault  = 45 * time.Second
	waitMax      = 300 * time.Second
	waitInterval = 15 * time.Second
)

// buildTerminalStates end processing.
var buildTerminalStates = map[string]bool{"VALID": true, "INVALID": true, "FAILED": true}

type buildsWaitOut struct {
	App             tool.App      `json:"app"`
	Platform        tool.Platform `json:"platform"`
	BuildID         string        `json:"build_id"`
	Number          string        `json:"number"`
	ProcessingState string        `json:"processing_state"`
	Uploaded        string        `json:"uploaded,omitempty"`
	Polls           int           `json:"polls"`
	WaitedSeconds   int           `json:"waited_seconds"`
}

func buildsWait(ctx context.Context, env *tool.Env, in buildsWaitIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	ref := in.Build
	if ref == "" {
		ref = tool.BuildLatest
	}
	build, err := env.Resolve.Build(ctx, app.ID, platform, ref)
	if err != nil {
		return nil, err
	}
	timeout := waitDefault
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, waitMax)
	}
	if !env.HasProgress() {
		timeout = min(timeout, tool.NoProgressWaitCap)
	}
	out := buildsWaitOut{App: app, Platform: platform, BuildID: build.ID, Number: build.Number}
	start := env.Clock.Now()
	q := url.Values{"fields[builds]": {"version,processingState,uploadedDate"}}
	for {
		resp, err := env.ASC.Get(ctx, "/v1/builds/"+build.ID, q)
		if err != nil {
			return nil, fmt.Errorf("get build %s: %w", build.ID, err)
		}
		out.Polls++
		if b, ok := resp.First(); ok {
			out.ProcessingState, out.Uploaded = b.Str("processingState"), b.Str("uploadedDate")
			if n := b.Str("version"); n != "" {
				out.Number = n
			}
		}
		waited := env.Clock.Now().Sub(start)
		out.WaitedSeconds = int(waited / time.Second)
		env.Progress(waited.Seconds(), timeout.Seconds(), fmt.Sprintf("build %s: %s", out.Number, out.ProcessingState))
		if buildTerminalStates[out.ProcessingState] {
			return waitDone(app, platform, out), nil
		}
		remaining := timeout - waited
		if remaining <= 0 {
			args := map[string]any{"app": app.ID, "platform": string(platform), "build": build.ID, "timeout_seconds": int(timeout / time.Second)}
			return tool.StillProcessing(out, "call builds_wait again with the same arguments").
				Then(tool.NextStep{Tool: "builds_wait", Args: args}), nil
		}
		if err := env.Clock.Sleep(ctx, min(waitInterval, remaining)); err != nil {
			return nil, err
		}
	}
}

// waitDone is the result of a build whose processing ended.
func waitDone(app tool.App, platform tool.Platform, out buildsWaitOut) *tool.Result {
	res := tool.OK(out)
	if out.ProcessingState != "VALID" {
		return res.Warn("build processing ended " + out.ProcessingState + "; App Store Connect emails the reason to the account holder. Fix and upload a new build").
			Then(tool.NextStep{Tool: "builds_list", Args: map[string]any{"app": app.ID, "platform": string(platform)}})
	}
	return res.Then(
		tool.NextStep{Tool: "testflight_distribute", Args: map[string]any{"app": app.ID, "build": out.BuildID}},
		statusNext(app, platform, "", map[string]any{"build": out.BuildID}),
	)
}
