package reports_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// The package's own -update flag clashes with tooltest's, so a minimal
// Portal fake lives here.
type dashCall struct{ Method, Path, Body string }

type dashPortal struct {
	t      *testing.T
	routes map[string]func() (string, error)
	calls  []dashCall
}

func newDashPortal(t *testing.T) *dashPortal {
	return &dashPortal{t: t, routes: map[string]func() (string, error){}}
}

func (p *dashPortal) HandleJSON(method, path, body string) {
	p.routes[method+" "+path] = func() (string, error) { return body, nil }
}

func (p *dashPortal) HandleError(method, path string, status int, msgs ...string) {
	p.routes[method+" "+path] = func() (string, error) { return "", &tool.PortalError{Status: status, Messages: msgs} }
}

func (p *dashPortal) Calls() []dashCall { return p.calls }

func (p *dashPortal) JSON(_ context.Context, method, path string, body, out any) error {
	c := dashCall{Method: method, Path: path}
	if body != nil {
		b, _ := json.Marshal(body)
		c.Body = string(b)
	}
	p.calls = append(p.calls, c)
	fn := p.routes[method+" "+path]
	if fn == nil {
		p.t.Fatalf("unexpected portal request %s %s", method, path)
	}
	resp, err := fn()
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp), out)
}

var (
	dashFrom = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	dashTo   = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
)

func TestDashboardDataEnd(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("GET", "/analytics/api/v1/settings/all", `{"measures":[],"configuration":{"dataStartDate":"2015-04-01T00:00:00Z","dataEndDate":"2026-10-26T00:00:00Z"}}`)
	got, err := reports.DataEnd(context.Background(), p)
	if err != nil || !got.Equal(dashTo) {
		t.Fatalf("DataEnd = %v, %v", got, err)
	}
}

func TestDashboardDataEndCorrupt(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("GET", "/analytics/api/v1/settings/all", `{"configuration":{}}`)
	if _, err := reports.DataEnd(context.Background(), p); !errors.Is(err, reports.ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}

func TestDashboardMeasures(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("POST", "/analytics/api/v1/data/app/detail/measures", `{"results":[
	 {"adamId":"9","measure":"impressionsTotal","type":"COUNT","total":30,"previousTotal":20,"percentChange":0.5,"meetsThreshold":true,
	  "data":[{"value":10,"date":"2026-10-25T00:00:00Z"},{"value":20,"date":"2026-10-26T00:00:00Z"}]},
	 {"adamId":"9","measure":"conversionRate","type":"AVERAGE","total":1.5,"previousTotal":0,"percentChange":0,"meetsThreshold":false,"data":[]}]}`)
	got, err := reports.Measures(context.Background(), p, "9", []string{"impressionsTotal", "conversionRate", "units"}, dashFrom, dashTo, "")
	if err != nil {
		t.Fatal(err)
	}
	imp := got["impressionsTotal"]
	if imp.Total != 30 || imp.PreviousTotal != 20 || imp.PercentChange != 0.5 || imp.Type != "COUNT" || !imp.MeetsThreshold ||
		len(imp.Points) != 2 || imp.Points[1] != (reports.DashboardPoint{Date: "2026-10-26", Value: 20}) {
		t.Errorf("impressions = %+v", imp)
	}
	if cr := got["conversionRate"]; cr.Type != "AVERAGE" || cr.Total != 1.5 || cr.MeetsThreshold {
		t.Errorf("conversion = %+v", cr)
	}
	if _, ok := got["units"]; ok {
		t.Error("measure Apple did not return is present")
	}
	body := p.Calls()[0].Body
	for _, want := range []string{`"adamId":["9"]`, `"startTime":"2026-10-20T00:00:00Z"`, `"endTime":"2026-10-26T00:00:00Z"`, `"frequency":"day"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
}

func TestDashboardNeverReleasedApp(t *testing.T) {
	p := newDashPortal(t)
	p.HandleError("POST", "/analytics/api/v1/data/app/detail/measures", 403, "Invalid adam id: 9")
	p.HandleError("POST", "/analytics/api/v1/data/timeseries", 403, "Make sure the request is permitted.")
	if _, err := reports.Measures(context.Background(), p, "9", []string{"units"}, dashFrom, dashTo, "day"); !errors.Is(err, reports.ErrDashboardNoData) {
		t.Errorf("measures err = %v", err)
	}
	// Another 403 stays an error.
	_, err := reports.Breakdown(context.Background(), p, "9", "units", "source", dashFrom, dashTo, 10)
	var pe *tool.PortalError
	if errors.Is(err, reports.ErrDashboardNoData) || !errors.As(err, &pe) || pe.Status != 403 {
		t.Errorf("breakdown err = %v", err)
	}
}

func TestDashboardBreakdownSortsAndDropsZeros(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("POST", "/analytics/api/v1/data/timeseries", `{"results":[
	 {"group":{"key":"AppRef","title":"App Referrer"},"totals":{"value":0,"type":"COUNT","key":"units"}},
	 {"group":{"key":"Other","title":"App Store Browse"},"totals":{"value":2,"type":"COUNT","key":"units"}},
	 {"group":{"key":"Search","title":"App Store Search"},"totals":{"value":7,"type":"COUNT","key":"units"}},
	 {"group":{"key":"Web","title":"Web Referrer"},"totals":{"value":2,"type":"COUNT","key":"units"}}]}`)
	got, err := reports.Breakdown(context.Background(), p, "9", "units", "source", dashFrom, dashTo, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []reports.DashboardGroup{{"Search", "App Store Search", 7}, {"Other", "App Store Browse", 2}, {"Web", "Web Referrer", 2}}
	if len(got) != len(want) {
		t.Fatalf("groups = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("groups[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if body := p.Calls()[0].Body; !strings.Contains(body, `"group":{"dimension":"source","limit":10,"metric":"units","rank":"DESCENDING"}`) {
		t.Errorf("body = %s", body)
	}
}

func TestFormatMeasure(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 148: "148", 1.18: "1.18", 12.346: "12.35"} {
		if got := reports.FormatMeasure(v); got != want {
			t.Errorf("FormatMeasure(%v) = %q, want %q", v, got, want)
		}
	}
}
