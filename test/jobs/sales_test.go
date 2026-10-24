package jobs

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// Job 4: "how were sales this week" in one Read call.
func TestJobSalesThisWeek(t *testing.T) {
	k := newKit(t)
	// Thursday 2026-10-22 12:00 Pacific: this_week = Mon 19 … Thu 22.
	k.Clock.Set(time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC))
	k.Resolve.Apps = append(k.Resolve.Apps, tool.App{ID: "1000000001", BundleID: "com.example.notes", Name: "Example Notes", SKU: "com.example.notes"})
	raw, err := os.ReadFile("../../internal/tools/reports/testdata/sales/sales_day1.tsv")
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
	gz := tooltest.Fixture{Header: http.Header{"Content-Type": {"application/a-gzip"}}, Body: b.Bytes()}
	k.ASC.HandleFixture(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-19", gz)
	k.ASC.HandleFixture(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-20", gz)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"The specified resource does not exist","detail":"There were no sales for the date specified."}]}`)

	j := connect(t, k)
	out := j.call("sales_report", `{"period":"this_week"}`)
	j.done(1)
	if out["units"] == nil {
		t.Fatalf("no units in sales report: %v", out)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("sales job mutated %d times", n)
	}
}
