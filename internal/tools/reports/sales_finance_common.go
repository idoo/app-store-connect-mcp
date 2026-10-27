package reports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
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
		Hint: fmt.Sprintf("run `app-store-connect-mcp auth set %s vendor_number=<number>` (or set ASC_VENDOR_NUMBER) and restart app-store-connect-mcp", profile),
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

// notFoundErr returns err as Apple's 404 (report not published, or no sales
// for the date), or nil for any other error.
func notFoundErr(err error) *tool.APIError {
	var api *tool.APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return api
	}
	return nil
}

// isNoSales reports whether a sales 404 means a zero-sales day rather than
// an unpublished report: only when Apple's message says there were no sales.
// Any other 404 stays pending whatever the date, since Apple sometimes
// publishes days late.
func isNoSales(api *tool.APIError) bool {
	return strings.Contains(strings.ToLower(api.Detail+" "+api.Title), "no sales")
}

// vendorCause adds the wrong-vendor-number cause to a sales/finance 403 or
// 5xx hint, keeping the existing cause: Apple answers a vendor number that
// is not this account's with exactly those statuses.
func vendorCause(err error, vendor string) error {
	var api *tool.APIError
	if !errors.As(err, &api) || (api.Status != http.StatusForbidden && api.Status < 500) {
		return err
	}
	cause := "or the vendor number " + vendor + " is not this account's: check App Store Connect → Payments and Financial Reports"
	if api.Hint == "" {
		api.Hint = cause
	} else {
		api.Hint += "; " + cause
	}
	return err
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
// (nil, apiErr, nil): the report is not published (or has no sales).
func download(ctx context.Context, env *tool.Env, path string, q url.Values) (*reports.Table, *tool.APIError, error) {
	body, _, err := env.ASC.Download(ctx, tool.Request{Method: http.MethodGet, Path: path, Query: q, Accept: gzipAccept})
	if err != nil {
		if nf := notFoundErr(err); nf != nil {
			return nil, nf, nil
		}
		err = vendorCause(err, q.Get("filter[vendorNumber]"))
		return nil, nil, fmt.Errorf("download %s %s: %w", path, q.Get("filter[reportDate]"), err)
	}
	t, err := reports.Decode(bytes.NewReader(body), reports.DefaultLimits())
	if err != nil {
		return nil, nil, fmt.Errorf("decode %s %s: %w", path, q.Get("filter[reportDate]"), err)
	}
	return t, nil, nil
}

// dayTable is the outcome of one report date in a fan-out. Pending means
// not published yet; NoSales means a published day without sales (no table).
type dayTable struct {
	Date    string
	Table   *reports.Table
	Pending bool
	NoSales bool
}

// fetchSalesDates downloads one sales report per date with bounded
// concurrency. Results keep the order of dates; 404s are marked no-sales
// (see isNoSales) or pending. The first real error cancels the rest.
func fetchSalesDates(ctx context.Context, env *tool.Env, vendor string, rs applemeta.ReportSpec, subType string, freq applemeta.Frequency, dates []string) ([]dayTable, error) {
	out := make([]dayTable, len(dates))
	err := tool.Parallel(ctx, reportFetchConcurrency, dates, func(ctx context.Context, i int, d string) error {
		t, nf, err := download(ctx, env, "/v1/salesReports", salesQuery(vendor, rs, subType, freq, d))
		if err != nil {
			return err
		}
		noSales := nf != nil && isNoSales(nf)
		out[i] = dayTable{Date: d, Table: t, Pending: nf != nil && !noSales, NoSales: noSales}
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

// currencyAmounts lists m's currencies sorted by currency code. Amounts in
// different currencies are never compared or summed: without exchange rates a
// larger number (JPY 420) does not mean a larger earner (USD 19.54).
func currencyAmounts(m reports.Money) []amountOut {
	curs := m.Currencies()
	out := make([]amountOut, 0, len(curs))
	for _, c := range curs {
		out = append(out, amountOut{Currency: c, Amount: reports.FormatExact(c, m[c])})
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
