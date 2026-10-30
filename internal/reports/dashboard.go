package reports

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// App Analytics dashboard endpoints, reached through [tool.Portal] with the
// human's web session. They answer for any key role, unlike the Analytics
// Reports API, but take exactly one app per request.
const (
	dashboardSettingsPath   = "/analytics/api/v1/settings/all"
	dashboardMeasuresPath   = "/analytics/api/v1/data/app/detail/measures"
	dashboardTimeseriesPath = "/analytics/api/v1/data/timeseries"
	dashboardTimeLayout     = "2006-01-02T15:04:05Z"
)

// ErrDashboardNoData means App Analytics has no data for the app: Apple
// answers 403 "Invalid adam id" for apps that were never on the App Store.
var ErrDashboardNoData = errors.New("reports: no App Analytics data for this app (never released on the App Store)")

// DashboardPoint is one dated value of a dashboard measure.
type DashboardPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// DashboardMeasure is one measure's totals over the requested window.
// PreviousTotal covers the equally long window just before it; Type is
// COUNT (summable) or AVERAGE (rates, averages).
type DashboardMeasure struct {
	Key            string
	Type           string
	Total          float64
	PreviousTotal  float64
	PercentChange  float64 // fraction (0.38 = +38%); 0 when the previous total is 0
	MeetsThreshold bool
	Points         []DashboardPoint
}

// DashboardGroup is one dimension value of a breakdown (key is Apple's id,
// title its display name, untrusted) with its total.
type DashboardGroup struct {
	Key   string
	Title string
	Total float64
}

// DataEnd returns the last day (UTC midnight) with App Analytics data.
func DataEnd(ctx context.Context, p tool.Portal) (time.Time, error) {
	var settings struct {
		Configuration struct {
			DataEndDate string `json:"dataEndDate"`
		} `json:"configuration"`
	}
	if err := p.JSON(ctx, "GET", dashboardSettingsPath, nil, &settings); err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, settings.Configuration.DataEndDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("reports: App Analytics settings: dataEndDate %q: %w", settings.Configuration.DataEndDate, ErrCorrupt)
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}

// Measures reads the totals of measures for one app over the inclusive days
// from..to at frequency (day, week or month; "" means day). Measures Apple
// does not return are absent from the map. A never-released app returns
// [ErrDashboardNoData].
func Measures(ctx context.Context, p tool.Portal, appID string, measures []string, from, to time.Time, frequency string) (map[string]DashboardMeasure, error) {
	body := map[string]any{
		"adamId":    []string{appID},
		"startTime": from.UTC().Format(dashboardTimeLayout),
		"endTime":   to.UTC().Format(dashboardTimeLayout),
		"measures":  measures,
		"frequency": cmp.Or(frequency, "day"),
	}
	var resp struct {
		Results []struct {
			Measure        string  `json:"measure"`
			Type           string  `json:"type"`
			Total          float64 `json:"total"`
			PreviousTotal  float64 `json:"previousTotal"`
			PercentChange  float64 `json:"percentChange"`
			MeetsThreshold bool    `json:"meetsThreshold"`
			Data           []struct {
				Date  string  `json:"date"`
				Value float64 `json:"value"`
			} `json:"data"`
		} `json:"results"`
	}
	if err := p.JSON(ctx, "POST", dashboardMeasuresPath, body, &resp); err != nil {
		return nil, dashboardErr(err)
	}
	out := make(map[string]DashboardMeasure, len(resp.Results))
	for _, r := range resp.Results {
		m := DashboardMeasure{
			Key: r.Measure, Type: r.Type, Total: r.Total, PreviousTotal: r.PreviousTotal,
			PercentChange: r.PercentChange, MeetsThreshold: r.MeetsThreshold,
		}
		for _, d := range r.Data {
			m.Points = append(m.Points, DashboardPoint{Date: dashboardDay(d.Date), Value: d.Value})
		}
		out[r.Measure] = m
	}
	return out, nil
}

// Breakdown reads metric for one app split by dimension (source,
// storefront, platform…) over the inclusive days from..to: the top limit
// groups Apple ranks, with zero totals dropped, sorted by total descending
// (then key). A never-released app returns [ErrDashboardNoData].
func Breakdown(ctx context.Context, p tool.Portal, appID, metric, dimension string, from, to time.Time, limit int) ([]DashboardGroup, error) {
	body := map[string]any{
		"adamId":    []string{appID},
		"measures":  []string{metric},
		"frequency": "day",
		"startTime": from.UTC().Format(dashboardTimeLayout),
		"endTime":   to.UTC().Format(dashboardTimeLayout),
		"group": map[string]any{
			"metric": metric, "dimension": dimension, "rank": "DESCENDING", "limit": limit,
		},
		"dimensionFilters": []any{},
	}
	var resp struct {
		Results []struct {
			Group struct {
				Key   string `json:"key"`
				Title string `json:"title"`
			} `json:"group"`
			Totals struct {
				Value float64 `json:"value"`
			} `json:"totals"`
		} `json:"results"`
	}
	if err := p.JSON(ctx, "POST", dashboardTimeseriesPath, body, &resp); err != nil {
		return nil, dashboardErr(err)
	}
	out := make([]DashboardGroup, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Totals.Value == 0 {
			continue
		}
		out = append(out, DashboardGroup{Key: r.Group.Key, Title: r.Group.Title, Total: r.Totals.Value})
	}
	slices.SortStableFunc(out, func(a, b DashboardGroup) int {
		if c := cmp.Compare(b.Total, a.Total); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return out, nil
}

// FormatMeasure renders a dashboard value: whole numbers without decimals,
// others with two.
func FormatMeasure(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// dashboardErr maps Apple's 403 "Invalid adam id" (never-released app) to
// [ErrDashboardNoData].
func dashboardErr(err error) error {
	var pe *tool.PortalError
	if errors.As(err, &pe) && pe.Status == 403 {
		for _, m := range pe.Messages {
			if strings.Contains(strings.ToLower(m), "invalid adam id") {
				return ErrDashboardNoData
			}
		}
	}
	return err
}

// dashboardDay trims an RFC 3339 dashboard timestamp to YYYY-MM-DD.
func dashboardDay(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.DateOnly)
	}
	return s
}
