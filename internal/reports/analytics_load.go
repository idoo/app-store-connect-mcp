package reports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// ErrAnalyticsNotSetUp reports that the app has no active analytics report
// request, so Apple produces no analytics reports for it.
var ErrAnalyticsNotSetUp = errors.New("analytics not set up")

// ReportUnavailableError reports that the analytics request lists no report
// named Name; Available holds the names Apple does list.
type ReportUnavailableError struct {
	Name      string
	Available []string
}

func (e *ReportUnavailableError) Error() string {
	return fmt.Sprintf("analytics report %q unavailable; available: %s", e.Name, strings.Join(e.Available, "; "))
}

// segmentMaxBytes caps one compressed segment download.
const segmentMaxBytes = 64 << 20

// AnalyticsRows is the merged rows of one analytics report over a date window.
type AnalyticsRows struct {
	Report      string `json:"report"`
	ReportID    string `json:"report_id"`
	RequestID   string `json:"request_id"`
	AccessType  string `json:"access_type"`
	Granularity string `json:"granularity"`
	// Columns are the first segment's columns; later segments are aligned
	// to them by name.
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Dates are the processing dates whose segments were fetched, ascending.
	Dates []string `json:"dates,omitempty"`
	// Missing are processing dates in the window left unfetched by the cap.
	Missing  []string `json:"missing,omitempty"`
	Segments int      `json:"segments"`
	Partial  bool     `json:"partial,omitempty"`
}

// Col returns the index of column name (normalized match), or -1.
func (a *AnalyticsRows) Col(name string) int {
	t := Table{Columns: a.Columns}
	return t.Col(name)
}

// LoadAnalytics reads report reportName of the app's active analytics
// request (ONGOING preferred, else ONE_TIME_SNAPSHOT) for the instances of
// granularity whose processing date lies in from..to (inclusive days), and
// merges at most maxSegments segments, oldest first. It only issues GETs and
// downloads segments without authentication. No active request returns
// [ErrAnalyticsNotSetUp]; an unknown report name a [*ReportUnavailableError].
func LoadAnalytics(ctx context.Context, env *tool.Env, appID, reportName, granularity string,
	from, to time.Time, maxSegments int,
) (*AnalyticsRows, error) {
	if granularity == "" {
		granularity = "DAILY"
	}
	reqs, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/analyticsReportRequests", tool.ListOptions{
		Fields:   map[string][]string{"analyticsReportRequests": {"accessType", "stoppedDueToInactivity"}},
		MaxItems: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("list analytics report requests: %w", err)
	}
	req, ok := activeAnalyticsRequest(reqs.Data)
	if !ok {
		return nil, ErrAnalyticsNotSetUp
	}
	out := &AnalyticsRows{Report: reportName, RequestID: req.ID, AccessType: req.Str("accessType"), Granularity: granularity}

	reportID, err := findAnalyticsReport(ctx, env, req.ID, reportName)
	if err != nil {
		return nil, err
	}
	out.ReportID = reportID

	insts, err := env.ASC.List(ctx, "/v1/analyticsReports/"+reportID+"/instances", tool.ListOptions{
		Query:    map[string][]string{"filter[granularity]": {granularity}},
		Fields:   map[string][]string{"analyticsReportInstances": {"granularity", "processingDate"}},
		MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list analytics report instances: %w", err)
	}
	lo, hi := from.Format(time.DateOnly), to.Format(time.DateOnly)
	var picked []tool.Resource
	for _, r := range insts.Data {
		if d := r.Str("processingDate"); d >= lo && d <= hi {
			picked = append(picked, r)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].Str("processingDate") < picked[j].Str("processingDate") })

	for i, inst := range picked {
		date := inst.Str("processingDate")
		if out.Segments >= maxSegments {
			for _, rest := range picked[i:] {
				out.Missing = append(out.Missing, rest.Str("processingDate"))
			}
			out.Partial = true
			break
		}
		segs, err := env.ASC.List(ctx, "/v1/analyticsReportInstances/"+inst.ID+"/segments", tool.ListOptions{
			Fields: map[string][]string{"analyticsReportSegments": {"checksum", "sizeInBytes", "url"}},
		})
		if err != nil {
			return nil, fmt.Errorf("list segments of instance %s: %w", inst.ID, err)
		}
		fetched := false
		for _, s := range segs.Data {
			if out.Segments >= maxSegments {
				out.Partial = true
				break
			}
			t, err := fetchAnalyticsSegment(ctx, env, s)
			if err != nil {
				return nil, err
			}
			out.Segments++
			fetched = true
			out.merge(t)
		}
		if fetched {
			out.Dates = append(out.Dates, date)
		}
		if out.Partial && !fetched {
			out.Missing = append(out.Missing, date)
		}
	}
	return out, nil
}

// merge appends t's rows aligned to a's columns by name.
func (a *AnalyticsRows) merge(t *Table) {
	if a.Columns == nil {
		a.Columns = t.Columns
		a.Rows = append(a.Rows, t.Rows...)
		return
	}
	idx := make([]int, len(a.Columns))
	for i, c := range a.Columns {
		idx[i] = t.Col(c)
	}
	for _, row := range t.Rows {
		aligned := make([]string, len(a.Columns))
		for i, j := range idx {
			if j >= 0 && j < len(row) {
				aligned[i] = row[j]
			}
		}
		a.Rows = append(a.Rows, aligned)
	}
}

// activeAnalyticsRequest prefers an active ONGOING request, else an active
// ONE_TIME_SNAPSHOT.
func activeAnalyticsRequest(reqs []tool.Resource) (tool.Resource, bool) {
	for _, access := range []string{"ONGOING", "ONE_TIME_SNAPSHOT"} {
		for _, r := range reqs {
			if r.Str("accessType") != access {
				continue
			}
			if stopped, _ := r.Bool("stoppedDueToInactivity"); stopped {
				continue
			}
			return r, true
		}
	}
	return tool.Resource{}, false
}

// findAnalyticsReport returns the id of the report named name
// (case-insensitive). When the filtered listing is empty it lists every
// report to name the available ones.
func findAnalyticsReport(ctx context.Context, env *tool.Env, requestID, name string) (string, error) {
	path := "/v1/analyticsReportRequests/" + requestID + "/reports"
	fields := map[string][]string{"analyticsReports": {"name", "category"}}
	res, err := env.ASC.List(ctx, path, tool.ListOptions{
		Query: map[string][]string{"filter[name]": {name}}, Fields: fields, MaxItems: 50,
	})
	if err != nil {
		return "", fmt.Errorf("list analytics reports: %w", err)
	}
	for _, r := range res.Data {
		if strings.EqualFold(r.Str("name"), name) {
			return r.ID, nil
		}
	}
	all, err := env.ASC.List(ctx, path, tool.ListOptions{Fields: fields, MaxItems: 500})
	if err != nil {
		return "", fmt.Errorf("list analytics reports: %w", err)
	}
	names := make([]string, 0, len(all.Data))
	for _, r := range all.Data {
		if strings.EqualFold(r.Str("name"), name) {
			return r.ID, nil
		}
		names = append(names, r.Str("name"))
	}
	sort.Strings(names)
	return "", &ReportUnavailableError{Name: name, Available: names}
}

func fetchAnalyticsSegment(ctx context.Context, env *tool.Env, s tool.Resource) (*Table, error) {
	u := s.Str("url")
	if u == "" {
		return nil, fmt.Errorf("segment %s has no url", s.ID)
	}
	resp, err := env.Fetch.Fetch(ctx, http.MethodGet, u, nil, nil, segmentMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("download segment %s: %w", s.ID, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("download segment %s: HTTP %d (segment URLs expire; call again)", s.ID, resp.Status)
	}
	t, err := Decode(bytes.NewReader(resp.Body), DefaultLimits())
	if err != nil {
		return nil, fmt.Errorf("decode segment %s: %w", s.ID, err)
	}
	return t, nil
}
