package reports

import (
	"context"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type fakeArtifacts struct{ got map[string][]byte }

func (a *fakeArtifacts) Put(_ context.Context, name, mime string, b []byte) (tool.Link, error) {
	a.got[name] = b
	return tool.Link{URI: "app-store-connect-mcp://artifacts/" + name, Name: name, MIMEType: mime, Size: int64(len(b))}, nil
}

func TestSalesReportIncludeRowsLinksCSV(t *testing.T) {
	k := thisWeekKit(t)
	art := &fakeArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"this_week","include_rows":true}`))
	got := tooltest.Decode[map[string]any](t, res)
	if len(res.Links) != 1 || got["rows_resource"] != res.Links[0].URI {
		t.Fatalf("links = %+v, rows_resource = %v", res.Links, got["rows_resource"])
	}
	csv := string(art.got[res.Links[0].Name])
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if !strings.HasPrefix(lines[0], "report_date,") || len(lines)-1 < int(got["rows"].(float64)) {
		t.Fatalf("csv (%d lines, rows %v):\n%s", len(lines), got["rows"], csv)
	}
	if !strings.HasPrefix(lines[1], "2026-10-19,") {
		t.Errorf("first row = %q", lines[1])
	}
}

func TestSalesReportIncludeRowsWithoutArtifactsWarns(t *testing.T) {
	k := thisWeekKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"this_week","include_rows":true}`))
	got := tooltest.Decode[map[string]any](t, res)
	if len(res.Links) != 0 || got["rows_resource"] != nil || got["units"] == nil {
		t.Fatalf("links = %+v, got = %s", res.Links, tooltest.JSON(t, got))
	}
	if w := strings.Join(res.Meta.Warnings, "|"); !strings.Contains(w, "not stored as a resource") {
		t.Errorf("warnings = %q", w)
	}
}

func TestSalesReportNoRowsArtifactByDefault(t *testing.T) {
	k := thisWeekKit(t)
	art := &fakeArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"this_week"}`))
	if len(res.Links) != 0 || len(art.got) != 0 || len(res.Meta.Warnings) != 0 {
		t.Fatalf("links = %+v, warnings = %v", res.Links, res.Meta.Warnings)
	}
}

func TestFinanceReportIncludeRowsLinksCSV(t *testing.T) {
	k := reportsKit(t)
	art := &fakeArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08","include_rows":true}`))
	got := tooltest.Decode[map[string]any](t, res)
	if len(res.Links) != 1 || got["rows_resource"] != res.Links[0].URI || got["proceeds"] == nil {
		t.Fatalf("links = %+v, got = %s", res.Links, tooltest.JSON(t, got))
	}
	csv := strings.TrimSpace(string(art.got["finance-financial-ZZ-2026-08.csv"]))
	if lines := strings.Split(csv, "\n"); !strings.HasPrefix(lines[0], "report_date,") || len(lines)-1 != int(got["rows"].(float64)) {
		t.Fatalf("csv:\n%s", csv)
	}
}

func TestFinanceReportOmittedProductsLinkRows(t *testing.T) {
	k := reportsKit(t)
	art := &fakeArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08","top_n":1}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["products_omitted"] == nil {
		t.Fatalf("fixture should omit products at top_n 1: %s", tooltest.JSON(t, got))
	}
	if len(res.Links) != 1 || got["rows_resource"] != res.Links[0].URI {
		t.Fatalf("links = %+v, rows_resource = %v", res.Links, got["rows_resource"])
	}
}
