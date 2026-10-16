package insights

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const funnelAppID = "1000000001"

// funnelArtifacts records artifact puts.
type funnelArtifacts struct {
	mu   sync.Mutex
	puts map[string][]byte
}

func (a *funnelArtifacts) Put(_ context.Context, name, mime string, b []byte) (tool.Link, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.puts[name] = b
	return tool.Link{URI: "app-store-connect-mcp://artifacts/1", Name: name, MIMEType: mime, Size: int64(len(b))}, nil
}

func funnelGz(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "funnel", name))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func funnelDoc(t *testing.T, items ...map[string]any) string {
	t.Helper()
	if items == nil {
		items = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"data": items, "links": map[string]string{"self": "https://api.appstoreconnect.apple.com/v1/x"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func funnelRes(typ, id string, attrs map[string]any) map[string]any {
	return map[string]any{"type": typ, "id": id, "attributes": attrs}
}

func funnelTool(t *testing.T) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup("analytics_funnel_report")
	if !ok {
		t.Fatal("analytics_funnel_report not registered")
	}
	return tl
}

func funnelKit(t *testing.T, requests string) (*tooltest.Kit, *funnelArtifacts) {
	t.Helper()
	k := tooltest.New(t).ReadOnly()
	k.Clock.Set(time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC))
	k.Resolve.Apps = []tool.App{{ID: funnelAppID, BundleID: "com.example.notes", Name: "Example Notes", SKU: "com.example.notes"}}
	art := &funnelArtifacts{puts: map[string][]byte{}}
	k.Env.Artifacts = art
	k.ASC.Handle(http.MethodGet, "/v1/apps/"+funnelAppID+"/analyticsReportRequests", http.StatusOK, requests)
	return k, art
}

// setUpFunnel registers the discovery and downloads reports with one
// previous-period and one current-period instance each.
func setUpFunnel(t *testing.T, k *tooltest.Kit) {
	t.Helper()
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReportRequests/req-1/reports", http.StatusOK, funnelDoc(t,
		funnelRes("analyticsReports", "rep-d", map[string]any{"name": reports.ReportDiscovery, "category": "APP_STORE_ENGAGEMENT"}),
		funnelRes("analyticsReports", "rep-l", map[string]any{"name": reports.ReportDownloads, "category": "COMMERCE"}),
	))
	seg := func(id string) map[string]any {
		return funnelRes("analyticsReportSegments", id, map[string]any{"url": "https://segments.example.com/" + id, "checksum": "x", "sizeInBytes": 10})
	}
	for _, r := range []struct{ rep, prefix, file string }{{"rep-d", "d", "discovery"}, {"rep-l", "l", "downloads"}} {
		k.ASC.Handle(http.MethodGet, "/v1/analyticsReports/"+r.rep+"/instances", http.StatusOK, funnelDoc(t,
			funnelRes("analyticsReportInstances", r.prefix+"-old", map[string]any{"granularity": "DAILY", "processingDate": "2026-09-01"}),
			funnelRes("analyticsReportInstances", r.prefix+"-cur", map[string]any{"granularity": "DAILY", "processingDate": "2026-10-22"}),
			funnelRes("analyticsReportInstances", r.prefix+"-prev", map[string]any{"granularity": "DAILY", "processingDate": "2026-10-15"}),
		))
		for _, w := range []string{"cur", "prev"} {
			id := r.prefix + "-" + w
			k.ASC.Handle(http.MethodGet, "/v1/analyticsReportInstances/"+id+"/segments", http.StatusOK, funnelDoc(t, seg(id+".gz")))
			k.Fetch.Handle(http.MethodGet, "https://segments.example.com/"+id+".gz", http.StatusOK, funnelGz(t, r.file+"_"+w+".tsv"))
		}
	}
}

const ongoingRequests = `{"data":[{"type":"analyticsReportRequests","id":"req-1","attributes":{"accessType":"ONGOING","stoppedDueToInactivity":false}}],"links":{"self":"https://api.appstoreconnect.apple.com/v1/x"}}`

func TestFunnelBySource(t *testing.T) {
	k, art := funnelKit(t, ongoingRequests)
	setUpFunnel(t, k)
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{"question":"sources","period":"last_7_days"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "ok" || tooltest.JSON(t, got["current"]) != `{"from":"2026-10-19","to":"2026-10-25"}` ||
		tooltest.JSON(t, got["previous"]) != `{"from":"2026-10-12","to":"2026-10-18"}` {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	if s := tooltest.JSON(t, got["funnel"]); s != `{"change":{"downloads":"+70.0%","impressions":"+100.0%","page_views":"+55.6%"},`+
		`"current":{"download_rate":"18.21%","downloads":"51","impressions":"1800","page_view_rate":"15.56%","page_views":"280"},`+
		`"previous":{"download_rate":"16.67%","downloads":"30","impressions":"900","page_view_rate":"20.00%","page_views":"180"}}` {
		t.Errorf("funnel = %s", s)
	}
	table := got["table"].([]any)
	if len(table) != 2 || tooltest.JSON(t, table[1]) != `{"current":{"download_rate":"10.00%","downloads":"5","impressions":"500","page_view_rate":"10.00%","page_views":"50"},`+
		`"key":"App Store browse","previous":{"downloads":"0","impressions":"0","page_views":"0"}}` {
		t.Errorf("table = %s", tooltest.JSON(t, table))
	}
	if !strings.Contains(tooltest.JSON(t, table[0]), `"key":"App Store search"`) {
		t.Errorf("table[0] = %s", tooltest.JSON(t, table[0]))
	}
	if s := tooltest.JSON(t, got["coverage"]); !strings.Contains(s, `"days_expected":7,"days_present":2`) || !strings.Contains(s, `"lag_days":5`) {
		t.Errorf("coverage = %s", s)
	}
	if got["segments"] != 4.0 {
		t.Errorf("segments = %v", got["segments"])
	}
	if len(r.Links) != 1 || len(art.puts) != 1 || !bytes.Contains(art.puts["analytics-funnel-"+funnelAppID+".json"], []byte("Redownload")) {
		t.Errorf("links = %+v puts = %d", r.Links, len(art.puts))
	}
	if strings.Contains(tooltest.JSON(t, got), "Redownload") {
		t.Error("raw rows inlined")
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/analyticsReportInstances/d-old/segments")); n != 0 {
		t.Error("out-of-window instance fetched")
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Errorf("mutations = %+v", m)
	}
}

func TestFunnelNotSetUp(t *testing.T) {
	k, _ := funnelKit(t, funnelDoc(t))
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "not_set_up" || !strings.Contains(got["message"].(string), "ONE_TIME_SNAPSHOT") {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if len(r.Next) != 1 || r.Next[0].Tool != "analytics_setup" || r.Next[0].Args["one_time_snapshot"] != true || r.Next[0].Args["app"] != funnelAppID {
		t.Errorf("next = %+v", r.Next)
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Errorf("mutations = %+v", m)
	}
}

func TestFunnelReportNameMismatch(t *testing.T) {
	k, _ := funnelKit(t, ongoingRequests)
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReportRequests/req-1/reports", http.StatusOK, funnelDoc(t,
		funnelRes("analyticsReports", "rep-x", map[string]any{"name": "App Sessions Standard", "category": "APP_USAGE"})))
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "unavailable" || got["missing_report"] != reports.ReportDiscovery || tooltest.JSON(t, got["available_reports"]) != `["App Sessions Standard"]` {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
}

func TestLoadAnalyticsSegmentCap(t *testing.T) {
	k, _ := funnelKit(t, ongoingRequests)
	setUpFunnel(t, k)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	rows, err := reports.LoadAnalytics(context.Background(), k.Env, funnelAppID, reports.ReportDiscovery, "DAILY", from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Partial || rows.Segments != 1 || strings.Join(rows.Dates, ",") != "2026-10-15" || strings.Join(rows.Missing, ",") != "2026-10-22" {
		t.Errorf("rows = %+v", rows)
	}
	if len(k.Fetch.Calls) != 1 {
		t.Errorf("fetch calls = %v", k.Fetch.Calls)
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Errorf("mutations = %+v", m)
	}
}
