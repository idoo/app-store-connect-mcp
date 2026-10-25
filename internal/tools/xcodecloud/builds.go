package xcodecloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_build_start", Toolset: tool.XcodeCloud, Class: tool.Write,
		Title: "Start an Xcode Cloud build",
		Description: "Start a build run of a workflow (name or id) on a branch, tag or open pull request number; without a source " +
			"Xcode Cloud uses the workflow's default. clean forces a clean build. Not idempotent: each call starts a new run.",
	}, buildStart))
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_build_status", Toolset: tool.XcodeCloud, Class: tool.Read, Idempotent: true,
		Title: "Xcode Cloud build status",
		Description: "Status of a build run (id, or latest of a workflow): progress, result, issue counts, actions, test summary, " +
			"artifacts (URL host only unless include_urls). wait:true polls until complete (default 45 s, max 300 s), else still_processing.",
	}, buildStatus))
}

// --- xcc_build_start ---

type buildStartIn struct {
	App         tool.AppRef `json:"app,omitempty" jsonschema:"app used to find the workflow by name; default: pinned app"`
	Workflow    string      `json:"workflow" jsonschema:"workflow name or id"`
	Branch      string      `json:"branch,omitempty" jsonschema:"branch name, e.g. main"`
	Tag         string      `json:"tag,omitempty" jsonschema:"tag name, e.g. v1.2.0"`
	PullRequest int         `json:"pull_request,omitempty" jsonschema:"open pull request number"`
	Clean       *bool       `json:"clean,omitempty" jsonschema:"clean build (default: the workflow setting)"`
}

type sourceOut struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type runOut struct {
	ID                string      `json:"id"`
	Number            int         `json:"number,omitempty"`
	ExecutionProgress string      `json:"execution_progress,omitempty"`
	CompletionStatus  string      `json:"completion_status,omitempty"`
	StartReason       string      `json:"start_reason,omitempty"`
	CancelReason      string      `json:"cancel_reason,omitempty"`
	CreatedDate       string      `json:"created_date,omitempty"`
	StartedDate       string      `json:"started_date,omitempty"`
	FinishedDate      string      `json:"finished_date,omitempty"`
	PullRequest       bool        `json:"pull_request_build,omitempty"`
	CommitSHA         string      `json:"commit_sha,omitempty"`
	CommitMessage     any         `json:"commit_message,omitempty"`
	Issues            *issueCount `json:"issues,omitempty"`
	WorkflowID        string      `json:"workflow_id,omitempty"`
}

type buildStartOut struct {
	Run      runOut      `json:"run"`
	Workflow workflowRef `json:"workflow"`
	Source   *sourceOut  `json:"source,omitempty"`
}

type workflowRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func buildStart(ctx context.Context, env *tool.Env, in buildStartIn) (*tool.Result, error) {
	n := 0
	for _, set := range []bool{in.Branch != "", in.Tag != "", in.PullRequest != 0} {
		if set {
			n++
		}
	}
	if n > 1 {
		return nil, tool.InvalidArgs("pass at most one of branch, tag and pull_request")
	}
	if in.PullRequest < 0 {
		return nil, tool.InvalidArgs("pull_request must be positive")
	}
	if strings.TrimSpace(in.Workflow) == "" {
		return nil, tool.InvalidArgs("workflow is required (name or id)")
	}
	wf, err := resolveWorkflow(ctx, env, in.App, in.Workflow, "xcc_build_start")
	if err != nil {
		return nil, err
	}
	if !wf.Enabled {
		return nil, tool.InvalidArgs("workflow %q is disabled; enable it in Xcode or App Store Connect", wf.Name)
	}
	var src *sourceOut
	if n == 1 {
		if wf.Repository == nil {
			return nil, tool.InvalidArgs("workflow %q has no repository to resolve the source in", wf.Name)
		}
		if src, err = resolveSource(ctx, env, wf.Repository.ID, in); err != nil {
			return nil, err
		}
	}
	rels := map[string]any{"workflow": relData("ciWorkflows", wf.ID)}
	if src != nil {
		if src.Kind == "PULL_REQUEST" {
			rels["pullRequest"] = relData("scmPullRequests", src.ID)
		} else {
			rels["sourceBranchOrTag"] = relData("scmGitReferences", src.ID)
		}
	}
	data := map[string]any{"type": "ciBuildRuns", "relationships": rels}
	if in.Clean != nil {
		data["attributes"] = map[string]any{"clean": *in.Clean}
	}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/ciBuildRuns", Body: map[string]any{"data": data}, Write: true})
	if err != nil {
		return nil, fmt.Errorf("start build of workflow %s: %w", wf.Name, err)
	}
	r, ok := resp.First()
	if !ok {
		return nil, fmt.Errorf("start build of workflow %s: %w: empty response", wf.Name, tool.ErrInternal)
	}
	run := flattenRun(r)
	return tool.OK(buildStartOut{Run: run, Workflow: workflowRef{ID: wf.ID, Name: wf.Name}, Source: src}).
		Then(tool.NextStep{Tool: "xcc_build_status", Args: map[string]any{"run": run.ID, "wait": true}}), nil
}

func relData(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

// resolveSource finds the git reference or open pull request named in in.
func resolveSource(ctx context.Context, env *tool.Env, repoID string, in buildStartIn) (*sourceOut, error) {
	base := "/v1/scmRepositories/" + url.PathEscape(repoID)
	if in.PullRequest != 0 {
		q := url.Values{"fields[scmPullRequests]": {"title,number,isClosed,sourceBranchName,destinationBranchName"}}
		res, err := env.ASC.List(ctx, base+"/pullRequests", tool.ListOptions{Query: q, MaxItems: 1000})
		if err != nil {
			return nil, fmt.Errorf("list pull requests: %w", err)
		}
		for _, r := range res.Data {
			if num, _ := r.Int("number"); num != in.PullRequest {
				continue
			}
			if closed, _ := r.Bool("isClosed"); closed {
				return nil, tool.InvalidArgs("pull request #%d is closed", in.PullRequest)
			}
			return &sourceOut{Kind: "PULL_REQUEST", ID: r.ID, Name: fmt.Sprintf("#%d %s", in.PullRequest, r.Str("sourceBranchName"))}, nil
		}
		return nil, &tool.ErrNotFound{Kind: "pull request", Ref: fmt.Sprintf("#%d", in.PullRequest), Hint: "Xcode Cloud lists open pull requests of the workflow repository only"}
	}
	kind, name, prefix := "BRANCH", strings.TrimSpace(in.Branch), "refs/heads/"
	if in.Tag != "" {
		kind, name, prefix = "TAG", strings.TrimSpace(in.Tag), "refs/tags/"
	}
	q := url.Values{"fields[scmGitReferences]": {"name,canonicalName,isDeleted,kind"}}
	res, err := env.ASC.List(ctx, base+"/gitReferences", tool.ListOptions{Query: q, MaxItems: 5000})
	if err != nil {
		return nil, fmt.Errorf("list git references: %w", err)
	}
	for _, r := range res.Data {
		if r.Str("kind") != kind {
			continue
		}
		if deleted, _ := r.Bool("isDeleted"); deleted {
			continue
		}
		if r.Str("name") == name || r.Str("canonicalName") == prefix+name || r.Str("canonicalName") == name {
			return &sourceOut{Kind: kind, ID: r.ID, Name: r.Str("name")}, nil
		}
	}
	return nil, &tool.ErrNotFound{
		Kind: strings.ToLower(kind), Ref: name,
		Hint: "the name must match a " + strings.ToLower(kind) + " Xcode Cloud has seen in the repository (push it first)",
	}
}

// --- xcc_build_status ---

type buildStatusIn struct {
	Run         string      `json:"run,omitempty" jsonschema:"build run id; default: latest run of workflow"`
	Workflow    string      `json:"workflow,omitempty" jsonschema:"workflow name or id, when run is not given; default: the app's only workflow"`
	App         tool.AppRef `json:"app,omitempty" jsonschema:"app used to find the workflow by name; default: pinned app"`
	Wait        bool        `json:"wait,omitempty" jsonschema:"poll until the run completes or wait_seconds pass"`
	WaitSeconds int         `json:"wait_seconds,omitempty" jsonschema:"maximum wait (default 45, max 300)"`
	IncludeURLs bool        `json:"include_urls,omitempty" jsonschema:"return full artifact download URLs (signed, short-lived)"`
}

type issueCount struct {
	Errors           int `json:"errors"`
	Warnings         int `json:"warnings"`
	AnalyzerWarnings int `json:"analyzer_warnings"`
	TestFailures     int `json:"test_failures"`
}

type artifactOut struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int    `json:"size,omitempty"`
	Host string `json:"host,omitempty"`
	URL  string `json:"url,omitempty"`
}

type testFailure struct {
	Class   string `json:"class,omitempty"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message any    `json:"message,omitempty"`
}

type testSummary struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	Failures []testFailure  `json:"failures,omitempty"`
}

type actionOut struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	Type              string        `json:"type"`
	ExecutionProgress string        `json:"execution_progress,omitempty"`
	CompletionStatus  string        `json:"completion_status,omitempty"`
	RequiredToPass    bool          `json:"required_to_pass,omitempty"`
	Issues            *issueCount   `json:"issues,omitempty"`
	Tests             *testSummary  `json:"tests,omitempty"`
	Artifacts         []artifactOut `json:"artifacts,omitempty"`
}

type buildStatusOut struct {
	Run     runOut      `json:"run"`
	Actions []actionOut `json:"actions"`
}

// Wait bounds and poll cadence.
const (
	defaultWait  = 45 * time.Second
	maxWait      = 300 * time.Second
	pollInterval = 15 * time.Second
	// maxDetailActions caps the actions whose artifacts/tests are read.
	maxDetailActions = 10
	maxFailures      = 10
)

func buildStatus(ctx context.Context, env *tool.Env, in buildStatusIn) (*tool.Result, error) {
	if in.WaitSeconds < 0 || in.WaitSeconds > int(maxWait/time.Second) {
		return nil, tool.InvalidArgs("wait_seconds must be between 1 and 300")
	}
	wait := defaultWait
	if in.WaitSeconds > 0 {
		wait = time.Duration(in.WaitSeconds) * time.Second
	}
	if !env.HasProgress() {
		wait = min(wait, tool.NoProgressWaitCap)
	}
	runID := strings.TrimSpace(in.Run)
	if runID == "" {
		// Without a workflow the app's only workflow is used; several are
		// listed by name and app.
		id, err := latestRun(ctx, env, in.App, in.Workflow)
		if err != nil {
			return nil, err
		}
		runID = id
	}
	start := env.Clock.Now()
	deadline := start.Add(wait)
	var run runOut
	for {
		r, err := readRun(ctx, env, runID)
		if err != nil {
			return nil, err
		}
		run = r
		now := env.Clock.Now()
		if !in.Wait || run.ExecutionProgress == "COMPLETE" || !now.Before(deadline) {
			break
		}
		env.Progress(now.Sub(start).Seconds(), wait.Seconds(), fmt.Sprintf("build run #%d: %s", run.Number, run.ExecutionProgress))
		if err := env.Clock.Sleep(ctx, min(pollInterval, deadline.Sub(now))); err != nil {
			return nil, fmt.Errorf("wait for build run %s: %w", runID, err)
		}
	}
	actions, err := readActions(ctx, env, runID, in.IncludeURLs)
	if err != nil {
		return nil, err
	}
	out := buildStatusOut{Run: run, Actions: actions}
	if in.Wait && run.ExecutionProgress != "COMPLETE" {
		return tool.StillProcessing(out, fmt.Sprintf("call xcc_build_status again with run=%q and wait:true", runID)), nil
	}
	return tool.OK(out), nil
}

// latestRun returns the id of the newest build run of a workflow.
func latestRun(ctx context.Context, env *tool.Env, app tool.AppRef, workflow string) (string, error) {
	wf, err := resolveWorkflow(ctx, env, app, workflow, "xcc_build_status")
	if err != nil {
		return "", err
	}
	resp, err := env.ASC.Get(ctx, "/v1/ciWorkflows/"+url.PathEscape(wf.ID)+"/buildRuns", url.Values{
		"fields[ciBuildRuns]": {"number,executionProgress"},
		"sort":                {"-number"},
		"limit":               {"1"},
	})
	if err != nil {
		return "", fmt.Errorf("list build runs of workflow %s: %w", wf.Name, err)
	}
	r, ok := resp.First()
	if !ok {
		return "", &tool.ErrNotFound{
			Kind: "build run", Ref: wf.Name, Hint: "the workflow has no runs yet",
			Next: []tool.NextStep{{Tool: "xcc_build_start", Args: map[string]any{"workflow": wf.ID}}},
		}
	}
	return r.ID, nil
}

// readRun reads one build run.
func readRun(ctx context.Context, env *tool.Env, id string) (runOut, error) {
	resp, err := env.ASC.Get(ctx, "/v1/ciBuildRuns/"+url.PathEscape(id), url.Values{"fields[ciBuildRuns]": {runFields}})
	notFound := &tool.ErrNotFound{Kind: "build run", Ref: id, Next: []tool.NextStep{{Tool: "xcc_workflows_list"}}}
	if tool.APIStatus(err) == http.StatusNotFound {
		return runOut{}, notFound
	}
	if err != nil {
		return runOut{}, fmt.Errorf("read build run %s: %w", id, err)
	}
	r, ok := resp.First()
	if !ok {
		return runOut{}, notFound
	}
	return flattenRun(r), nil
}

// flattenRun builds a runOut from a ciBuildRuns resource.
func flattenRun(r tool.Resource) runOut {
	run := runOut{
		ID: r.ID, ExecutionProgress: r.Str("executionProgress"), CompletionStatus: r.Str("completionStatus"),
		StartReason: r.Str("startReason"), CancelReason: r.Str("cancelReason"), CreatedDate: r.Str("createdDate"),
		StartedDate: r.Str("startedDate"), FinishedDate: r.Str("finishedDate"), WorkflowID: r.RelID("workflow"),
		Issues: issues(r.Attributes["issueCounts"]),
	}
	run.Number, _ = r.Int("number")
	run.PullRequest, _ = r.Bool("isPullRequestBuild")
	if c, ok := tool.Unwrap(r.Attributes["sourceCommit"]).(map[string]any); ok {
		run.CommitSHA, _ = c["commitSha"].(string)
		if msg, _ := c["message"].(string); msg != "" {
			run.CommitMessage = tool.Untrusted(msg)
		}
	}
	return run
}

// issues decodes CiIssueCounts.
func issues(v any) *issueCount {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	n := func(k string) int {
		f, _ := m[k].(float64)
		return int(f)
	}
	return &issueCount{Errors: n("errors"), Warnings: n("warnings"), AnalyzerWarnings: n("analyzerWarnings"), TestFailures: n("testFailures")}
}

// readActions reads a run's actions and, for completed ones, their
// artifacts and (for TEST actions) test results.
func readActions(ctx context.Context, env *tool.Env, runID string, includeURLs bool) ([]actionOut, error) {
	q := url.Values{"fields[ciBuildActions]": {"name,actionType,startedDate,finishedDate,issueCounts,executionProgress,completionStatus,isRequiredToPass"}}
	res, err := env.ASC.List(ctx, "/v1/ciBuildRuns/"+url.PathEscape(runID)+"/actions", tool.ListOptions{Query: q, MaxItems: 50})
	if err != nil {
		return nil, fmt.Errorf("list actions of build run %s: %w", runID, err)
	}
	out := make([]actionOut, 0, len(res.Data))
	var detail []int // indexes into out of completed actions to detail
	for _, r := range res.Data {
		a := actionOut{
			ID: r.ID, Name: r.Str("name"), Type: r.Str("actionType"), ExecutionProgress: r.Str("executionProgress"),
			CompletionStatus: r.Str("completionStatus"), Issues: issues(r.Attributes["issueCounts"]),
		}
		a.RequiredToPass, _ = r.Bool("isRequiredToPass")
		if a.ExecutionProgress == "COMPLETE" && len(detail) < maxDetailActions {
			detail = append(detail, len(out))
		}
		out = append(out, a)
	}
	err = tool.Parallel(ctx, fanOut, detail, func(ctx context.Context, _ int, idx int) error {
		a := &out[idx]
		var err error
		if a.Artifacts, err = readArtifacts(ctx, env, a.ID, includeURLs); err != nil {
			return err
		}
		if a.Type == "TEST" {
			a.Tests, err = readTests(ctx, env, a.ID)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func readArtifacts(ctx context.Context, env *tool.Env, actionID string, includeURLs bool) ([]artifactOut, error) {
	q := url.Values{"fields[ciArtifacts]": {"fileType,fileName,fileSize,downloadUrl"}}
	res, err := env.ASC.List(ctx, "/v1/ciBuildActions/"+url.PathEscape(actionID)+"/artifacts", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return nil, fmt.Errorf("list artifacts of action %s: %w", actionID, err)
	}
	out := make([]artifactOut, 0, len(res.Data))
	for _, r := range res.Data {
		a := artifactOut{Name: r.Str("fileName"), Type: r.Str("fileType")}
		a.Size, _ = r.Int("fileSize")
		if raw := r.Str("downloadUrl"); raw != "" {
			if includeURLs {
				a.URL = raw
			} else if u, err := url.Parse(raw); err == nil {
				a.Host = u.Host
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func readTests(ctx context.Context, env *tool.Env, actionID string) (*testSummary, error) {
	q := url.Values{"fields[ciTestResults]": {"className,name,status,message"}}
	res, err := env.ASC.List(ctx, "/v1/ciBuildActions/"+url.PathEscape(actionID)+"/testResults", tool.ListOptions{Query: q, MaxItems: 5000})
	if err != nil {
		return nil, fmt.Errorf("list test results of action %s: %w", actionID, err)
	}
	s := &testSummary{ByStatus: map[string]int{}}
	for _, r := range res.Data {
		st := r.Str("status")
		s.Total++
		s.ByStatus[st]++
		if (st == "FAILURE" || st == "MIXED") && len(s.Failures) < maxFailures {
			f := testFailure{Class: r.Str("className"), Name: r.Str("name"), Status: st}
			if m := r.Str("message"); m != "" {
				f.Message = tool.Untrusted(m)
			}
			s.Failures = append(s.Failures, f)
		}
	}
	return s, nil
}
