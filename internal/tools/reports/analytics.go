package reports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/reports"
	"github.com/idoo/hamn/internal/tool"
)

const (
	analyticsDefaultSegments = 3
	analyticsMaxSegments     = 10
	analyticsDefaultTopN     = 20
	analyticsMaxTopN         = 200
	// analyticsSegmentMaxBytes caps one compressed segment download.
	analyticsSegmentMaxBytes = 64 << 20
)

var analyticsCategories = []any{"APP_USAGE", "APP_STORE_ENGAGEMENT", "COMMERCE", "FRAMEWORK_USAGE", "PERFORMANCE"}

type analyticsSetupIn struct {
	App             tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	OneTimeSnapshot bool        `json:"one_time_snapshot,omitempty" jsonschema:"also request a ONE_TIME_SNAPSHOT (history)"`
}

type analyticsReportIn struct {
	App         tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	AccessType  string      `json:"access_type,omitempty" jsonschema:"default ONGOING, else ONE_TIME_SNAPSHOT"`
	Category    string      `json:"category,omitempty" jsonschema:"only reports of this category"`
	Report      string      `json:"report,omitempty" jsonschema:"report id or name (unique substring); omit to list"`
	Granularity string      `json:"granularity,omitempty" jsonschema:"default DAILY"`
	From        string      `json:"from,omitempty" jsonschema:"first processing date YYYY-MM-DD"`
	To          string      `json:"to,omitempty" jsonschema:"last processing date YYYY-MM-DD"`
	Dimensions  []string    `json:"dimensions,omitempty" jsonschema:"columns to group by (see result columns)"`
	Metrics     []string    `json:"metrics,omitempty" jsonschema:"columns to sum; default all numeric"`
	TopN        int         `json:"top_n,omitempty" jsonschema:"groups kept, default 20, max 200"`
	MaxSegments int         `json:"max_segments,omitempty" jsonschema:"newest segments to download; default 3, max 10"`
	RawRows     int         `json:"raw_rows,omitempty" jsonschema:"also return up to this many raw rows (max 500)"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "analytics_setup", Toolset: tool.Reports, Class: tool.Write, Idempotent: true,
		Title: "Enable App Analytics reports",
		Description: "Ensures an ONGOING analytics report request exists (optional ONE_TIME_SNAPSHOT for history); " +
			"existing ones report already. Data arrives 24–48 h later via analytics_report.",
	}, analyticsSetup))
	tool.Register(tool.New(tool.Meta{
		Name: "analytics_report", Toolset: tool.Reports, Class: tool.Read, Idempotent: true,
		Title: "App Analytics report",
		Description: "Without report: lists the app's analytics reports. With report: sums metrics of its newest segments " +
			"by dimensions. Needs analytics_setup; data lags 24–48 h.",
		Enums: map[string][]any{
			"access_type": {accessOngoing, accessSnapshot},
			"category":    analyticsCategories,
			"granularity": {"DAILY", "WEEKLY", "MONTHLY"},
		},
	}, analyticsReport))
}

// requestOut is one analytics report request in a result.
type requestOut struct {
	ID         string `json:"id"`
	AccessType string `json:"access_type"`
	Status     string `json:"status"` // created | already | stopped_recreated
}

func analyticsSetup(ctx context.Context, env *tool.Env, in analyticsSetupIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	reqs, err := analyticsRequests(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	want := []string{accessOngoing}
	if in.OneTimeSnapshot {
		want = append(want, accessSnapshot)
	}
	var out []requestOut
	created := false
	for _, access := range want {
		if r, ok := activeRequest(reqs, access); ok {
			out = append(out, requestOut{ID: r.ID, AccessType: access, Status: "already"})
			continue
		}
		stopped := slices.ContainsFunc(reqs, func(r tool.Resource) bool { return r.Str("accessType") == access })
		r, already, err := createAnalyticsRequest(ctx, env, app.ID, access)
		if err != nil {
			return nil, err
		}
		status := "created"
		switch {
		case already:
			status = "already"
		case stopped:
			status = "stopped_recreated"
		}
		created = created || !already
		out = append(out, requestOut{ID: r.ID, AccessType: access, Status: status})
	}
	data := map[string]any{"app": app, "requests": out, "note": analyticsLagNote}
	next := tool.NextStep{Tool: "analytics_report", Args: appIDArgs(app.ID)}
	if !created {
		return tool.Already(data).Then(next), nil
	}
	return tool.OK(data).Then(next), nil
}

// createAnalyticsRequest POSTs a request. A 409 (Apple already has one) is
// read back and reported as already.
func createAnalyticsRequest(ctx context.Context, env *tool.Env, appID, access string) (tool.Resource, bool, error) {
	resp, err := env.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/analyticsReportRequests", Write: true,
		Body: map[string]any{"data": map[string]any{
			"type":       "analyticsReportRequests",
			"attributes": map[string]any{"accessType": access},
			"relationships": map[string]any{
				"app": map[string]any{"data": map[string]any{"type": "apps", "id": appID}},
			},
		}},
	})
	var api *tool.APIError
	if errors.As(err, &api) && api.Status == http.StatusConflict {
		reqs, rerr := analyticsRequests(ctx, env, appID)
		if rerr != nil {
			return tool.Resource{}, false, rerr
		}
		if r, ok := activeRequest(reqs, access); ok {
			return r, true, nil
		}
		return tool.Resource{}, false, fmt.Errorf("create %s analytics request: %w", access, err)
	}
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("create %s analytics request: %w", access, err)
	}
	r, ok := resp.First()
	if !ok {
		return tool.Resource{}, false, fmt.Errorf("create %s analytics request: %w: empty response", access, tool.ErrOutcomeUnknown)
	}
	return r, false, nil
}

// reportOut is one analytics report in a result.
type reportOut struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// instanceOut is one analytics report instance in a result.
type instanceOut struct {
	ID             string `json:"id"`
	ProcessingDate string `json:"processing_date"`
	Granularity    string `json:"granularity"`
}

type analyticsOut struct {
	App               tool.App                `json:"app"`
	RequestID         string                  `json:"request_id"`
	AccessType        string                  `json:"access_type"`
	Reports           []reportOut             `json:"reports,omitempty"`
	Report            *reportOut              `json:"report,omitempty"`
	Instances         []instanceOut           `json:"instances,omitempty"`
	SegmentsFetched   int                     `json:"segments_fetched,omitempty"`
	SegmentsSkipped   int                     `json:"segments_skipped,omitempty"`
	InstancesNotFetch int                     `json:"instances_not_fetched,omitempty"`
	Columns           []string                `json:"columns,omitempty"`
	Summary           *reports.SegmentSummary `json:"summary,omitempty"`
}

func analyticsReport(ctx context.Context, env *tool.Env, in analyticsReportIn) (*tool.Result, error) {
	if err := checkAnalyticsArgs(&in); err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	reqs, err := analyticsRequests(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	req, ok := pickRequest(reqs, in.AccessType)
	if !ok {
		return nil, analyticsNotSetUp(env, app.ID)
	}
	out := analyticsOut{App: app, RequestID: req.ID, AccessType: req.Str("accessType")}

	all, err := listAnalyticsReports(ctx, env, req.ID, in.Category)
	if err != nil {
		return nil, err
	}
	if in.Report == "" {
		out.Reports = all
		res := tool.OK(&out)
		if len(all) == 0 {
			res.Warn("no reports yet; " + analyticsLagNote)
		}
		return res, nil
	}
	rep, err := matchReport(all, in.Report)
	if err != nil {
		return nil, err
	}
	out.Report = &rep

	insts, err := listInstances(ctx, env, rep.ID, in)
	if err != nil {
		return nil, err
	}
	out.Instances = insts
	res := tool.OK(&out)
	if len(insts) == 0 {
		res.Warn("no " + in.Granularity + " instances in range yet; " + analyticsLagNote)
		return res, nil
	}
	merged, err := fetchSegments(ctx, env, insts, in.MaxSegments, &out, res)
	if err != nil {
		return nil, err
	}
	if merged == nil {
		return res, nil
	}
	out.Columns = merged.Columns
	metrics := in.Metrics
	if len(metrics) == 0 {
		metrics = numericColumns(merged)
	}
	for _, c := range slices.Concat(in.Dimensions, metrics) {
		if merged.Col(c) < 0 {
			return nil, fmt.Errorf("%w: unknown column %q; columns: %s", tool.ErrInvalidArgs, c, strings.Join(merged.Columns, ", "))
		}
	}
	if len(metrics) == 0 {
		return nil, fmt.Errorf("%w: no numeric column found; pass metrics (columns: %s)", tool.ErrInvalidArgs, strings.Join(merged.Columns, ", "))
	}
	sum, err := reports.SummarizeSegment(merged, in.Dimensions, metrics, in.TopN, in.RawRows)
	if err != nil {
		return nil, fmt.Errorf("summarize %s: %w", rep.Name, err)
	}
	out.Summary = sum
	return res, nil
}

// checkAnalyticsArgs validates and defaults analytics_report arguments.
func checkAnalyticsArgs(in *analyticsReportIn) error {
	for _, d := range []string{in.From, in.To} {
		if d == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return fmt.Errorf("%w: from/to must be YYYY-MM-DD", tool.ErrInvalidArgs)
		}
	}
	if in.From != "" && in.To != "" && in.To < in.From {
		return fmt.Errorf("%w: to is before from", tool.ErrInvalidArgs)
	}
	if in.RawRows < 0 || in.TopN < 0 || in.MaxSegments < 0 {
		return fmt.Errorf("%w: top_n, raw_rows and max_segments must not be negative", tool.ErrInvalidArgs)
	}
	if in.Granularity == "" {
		in.Granularity = "DAILY"
	}
	if in.TopN == 0 {
		in.TopN = analyticsDefaultTopN
	}
	in.TopN = min(in.TopN, analyticsMaxTopN)
	if in.MaxSegments == 0 {
		in.MaxSegments = analyticsDefaultSegments
	}
	in.MaxSegments = min(in.MaxSegments, analyticsMaxSegments)
	in.RawRows = min(in.RawRows, reports.MaxRawRows)
	return nil
}

// pickRequest chooses the request to read: the requested access type, else
// an active ONGOING request, else an active snapshot.
func pickRequest(reqs []tool.Resource, access string) (tool.Resource, bool) {
	if access != "" {
		return activeRequest(reqs, access)
	}
	if r, ok := activeRequest(reqs, accessOngoing); ok {
		return r, true
	}
	return activeRequest(reqs, accessSnapshot)
}

func listAnalyticsReports(ctx context.Context, env *tool.Env, requestID, category string) ([]reportOut, error) {
	opts := tool.ListOptions{
		Fields:   map[string][]string{"analyticsReports": {"name", "category"}},
		MaxItems: 500,
	}
	if category != "" {
		opts.Query = map[string][]string{"filter[category]": {category}}
	}
	res, err := env.ASC.List(ctx, "/v1/analyticsReportRequests/"+requestID+"/reports", opts)
	if err != nil {
		return nil, fmt.Errorf("list analytics reports: %w", err)
	}
	out := make([]reportOut, 0, len(res.Data))
	for _, r := range res.Data {
		out = append(out, reportOut{ID: r.ID, Name: r.Str("name"), Category: r.Str("category")})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// matchReport finds a report by id, exact name (case-insensitive) or unique
// name substring.
func matchReport(all []reportOut, ref string) (reportOut, error) {
	var subs []reportOut
	for _, r := range all {
		if r.ID == ref || strings.EqualFold(r.Name, ref) {
			return r, nil
		}
		if strings.Contains(strings.ToLower(r.Name), strings.ToLower(ref)) {
			subs = append(subs, r)
		}
	}
	if len(subs) == 1 {
		return subs[0], nil
	}
	names := make([]string, 0, len(subs))
	for _, r := range subs {
		names = append(names, r.Name)
	}
	if len(subs) > 1 {
		return reportOut{}, fmt.Errorf("%w: report %q matches %d reports: %s", tool.ErrInvalidArgs, ref, len(subs), strings.Join(names, "; "))
	}
	return reportOut{}, fmt.Errorf("%w: no report matches %q; call analytics_report without report to list them", tool.ErrInvalidArgs, ref)
}

// listInstances returns the report's instances of the granularity within
// from/to, newest first.
func listInstances(ctx context.Context, env *tool.Env, reportID string, in analyticsReportIn) ([]instanceOut, error) {
	res, err := env.ASC.List(ctx, "/v1/analyticsReports/"+reportID+"/instances", tool.ListOptions{
		Query:    map[string][]string{"filter[granularity]": {in.Granularity}},
		Fields:   map[string][]string{"analyticsReportInstances": {"granularity", "processingDate"}},
		MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list analytics report instances: %w", err)
	}
	out := []instanceOut{}
	for _, r := range res.Data {
		d := r.Str("processingDate")
		if (in.From != "" && d < in.From) || (in.To != "" && d > in.To) {
			continue
		}
		out = append(out, instanceOut{ID: r.ID, ProcessingDate: d, Granularity: r.Str("granularity")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ProcessingDate > out[j].ProcessingDate })
	return out, nil
}

// fetchSegments downloads up to maxSegments segments of insts (newest
// first) without authentication and merges their rows. Segments whose
// columns differ from the first are skipped with a warning.
func fetchSegments(ctx context.Context, env *tool.Env, insts []instanceOut, maxSegments int, out *analyticsOut, res *tool.Result) (*reports.Table, error) {
	var merged *reports.Table
	for i, inst := range insts {
		if out.SegmentsFetched >= maxSegments {
			out.InstancesNotFetch = len(insts) - i
			break
		}
		segs, err := env.ASC.List(ctx, "/v1/analyticsReportInstances/"+inst.ID+"/segments", tool.ListOptions{
			Fields: map[string][]string{"analyticsReportSegments": {"checksum", "sizeInBytes", "url"}},
		})
		if err != nil {
			return nil, fmt.Errorf("list segments of instance %s: %w", inst.ID, err)
		}
		for _, s := range segs.Data {
			if out.SegmentsFetched >= maxSegments {
				out.SegmentsSkipped++
				continue
			}
			t, err := fetchSegment(ctx, env, s)
			if err != nil {
				return nil, err
			}
			out.SegmentsFetched++
			switch {
			case merged == nil:
				merged = t
			case slices.EqualFunc(merged.Columns, t.Columns, strings.EqualFold):
				merged.Rows = append(merged.Rows, t.Rows...)
			default:
				res.Warn(fmt.Sprintf("segment %s of %s has different columns; skipped", s.ID, inst.ProcessingDate))
			}
		}
	}
	if out.SegmentsSkipped > 0 || out.InstancesNotFetch > 0 {
		res.Warn(fmt.Sprintf("segment cap %d reached; raise max_segments or narrow from/to", maxSegments))
	}
	return merged, nil
}

func fetchSegment(ctx context.Context, env *tool.Env, s tool.Resource) (*reports.Table, error) {
	u := s.Str("url")
	if u == "" {
		return nil, fmt.Errorf("segment %s has no url", s.ID)
	}
	resp, err := env.Fetch.Fetch(ctx, http.MethodGet, u, nil, nil, analyticsSegmentMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("download segment %s: %w", s.ID, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("download segment %s: HTTP %d (segment URLs expire; call again)", s.ID, resp.Status)
	}
	t, err := reports.Decode(bytes.NewReader(resp.Body), reports.DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("decode segment %s: %w", s.ID, err)
	}
	return t, nil
}

// numericColumns returns the columns whose non-empty cells are all decimal
// numbers, skipping identifier, date and version columns.
func numericColumns(t *reports.Table) []string {
	var out []string
	for i, c := range t.Columns {
		lc := strings.ToLower(c)
		if strings.Contains(lc, "identifier") || strings.Contains(lc, "date") || strings.Contains(lc, "version") || strings.HasSuffix(lc, " id") {
			continue
		}
		seen, ok := false, true
		for _, row := range t.Rows {
			if i >= len(row) || strings.TrimSpace(row[i]) == "" {
				continue
			}
			if _, valid := new(big.Rat).SetString(strings.TrimSpace(row[i])); !valid {
				ok = false
				break
			}
			seen = true
		}
		if ok && seen {
			out = append(out, c)
		}
	}
	return out
}
