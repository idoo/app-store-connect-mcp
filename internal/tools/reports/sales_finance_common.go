package reports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/reports"
	"github.com/idoo/hamn/internal/tool"
)

const (
	// reportFetchConcurrency bounds parallel report downloads per call.
	reportFetchConcurrency = 4
	// gzipAccept is the media type Apple serves sales and finance reports as.
	gzipAccept = "application/a-gzip"
)

// vendorNumberMissing is the typed error for profiles without a vendor number.
func vendorNumberMissing(profile string) error {
	if profile == "" {
		profile = "<profile>"
	}
	return &tool.Error{
		Code: "vendor_number_missing",
		Message: "sales and finance reports need the vendor number of the account, which the API does not expose; " +
			"find it in App Store Connect → Payments and Financial Reports (top-left, under the account name)",
		Hint: fmt.Sprintf("run `hamn auth set %s vendor_number=<number>` (or set ASC_VENDOR_NUMBER) and restart hamn", profile),
	}
}

// requireVendor returns the profile's vendor number or the typed error.
func requireVendor(env *tool.Env) (string, error) {
	v := strings.TrimSpace(env.Profile.VendorNumber)
	if v == "" {
		return "", vendorNumberMissing(env.Profile.Name)
	}
	return v, nil
}

// isPending reports whether err is Apple's 404 "report not available yet".
// Only 404 maps to pending; any other status is a real error.
func isPending(err error) bool {
	var api *tool.APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

// salesQuery builds the /v1/salesReports filters for one report.
func salesQuery(vendor string, rs applemeta.ReportSpec, subType string, freq applemeta.Frequency, date string) url.Values {
	return url.Values{
		"filter[vendorNumber]":  {vendor},
		"filter[reportType]":    {string(rs.Type)},
		"filter[reportSubType]": {subType},
		"filter[frequency]":     {string(freq)},
		"filter[reportDate]":    {date},
		"filter[version]":       {rs.Version},
	}
}

// financeQuery builds the /v1/financeReports filters for one fiscal month.
func financeQuery(vendor string, rt applemeta.ReportType, region, month string) url.Values {
	return url.Values{
		"filter[vendorNumber]": {vendor},
		"filter[reportType]":   {string(rt)},
		"filter[regionCode]":   {region},
		"filter[reportDate]":   {month},
	}
}

// download fetches one gzip report and decodes it. A 404 returns
// (nil, true, nil): the report is not published yet.
func download(ctx context.Context, env *tool.Env, path string, q url.Values) (*reports.Table, bool, error) {
	body, _, err := env.ASC.Download(ctx, tool.Request{Method: http.MethodGet, Path: path, Query: q, Accept: gzipAccept})
	if err != nil {
		if isPending(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("download %s %s: %w", path, q.Get("filter[reportDate]"), err)
	}
	t, err := reports.Decode(bytes.NewReader(body), reports.DefaultLimits())
	if err != nil {
		return nil, false, fmt.Errorf("decode %s %s: %w", path, q.Get("filter[reportDate]"), err)
	}
	return t, false, nil
}

// dayTable is the outcome of one report date in a fan-out.
type dayTable struct {
	Date    string
	Table   *reports.Table
	Pending bool
}

// fetchSalesDates downloads one sales report per date with bounded
// concurrency. Results keep the order of dates; 404s are marked pending. The
// first real error cancels the rest.
func fetchSalesDates(ctx context.Context, env *tool.Env, vendor string, rs applemeta.ReportSpec, subType string, freq applemeta.Frequency, dates []string) ([]dayTable, error) {
	out := make([]dayTable, len(dates))
	err := tool.Parallel(ctx, reportFetchConcurrency, dates, func(ctx context.Context, i int, d string) error {
		t, pending, err := download(ctx, env, "/v1/salesReports", salesQuery(vendor, rs, subType, freq, d))
		if err != nil {
			return err
		}
		out[i] = dayTable{Date: d, Table: t, Pending: pending}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lastClosedMonth returns the fiscal month (YYYY-MM) before now's Pacific
// month, shifted back by extra months.
func lastClosedMonth(now time.Time, extra int) string {
	d := applemeta.PacificDay(now)
	first := d.AddDate(0, 0, 1-d.Day())
	return first.AddDate(0, -1-extra, 0).Format("2006-01")
}

// amountOut is one currency amount, formatted with its precision.
type amountOut struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// sortedAmounts lists m's currencies by amount, largest first (ties by code).
// Amounts in different currencies are compared numerically only to order the
// list; they are never summed.
func sortedAmounts(m reports.Money) []amountOut {
	curs := m.Currencies()
	sort.SliceStable(curs, func(i, j int) bool {
		if c := m[curs[i]].Cmp(m[curs[j]]); c != 0 {
			return c > 0
		}
		return curs[i] < curs[j]
	})
	out := make([]amountOut, 0, len(curs))
	for _, c := range curs {
		out = append(out, amountOut{Currency: c, Amount: reports.FormatAmount(c, m[c])})
	}
	return out
}

// tableRows renders up to limit table rows as column→value objects.
func tableRows(t *reports.Table, rows [][]string, limit int) []map[string]string {
	n := min(len(rows), limit)
	out := make([]map[string]string, 0, n)
	for _, r := range rows[:n] {
		m := make(map[string]string, len(t.Columns))
		for i, c := range t.Columns {
			if i < len(r) && r[i] != "" {
				m[c] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}
