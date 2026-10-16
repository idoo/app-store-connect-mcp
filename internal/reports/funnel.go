package reports

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// Window is an inclusive range of days (YYYY-MM-DD).
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Contains reports whether day lies in w.
func (w Window) Contains(day string) bool { return day >= w.From && day <= w.To }

// Days returns the inclusive day count of w (0 when unparsable).
func (w Window) Days() int {
	f, err1 := time.Parse(time.DateOnly, w.From)
	t, err2 := time.Parse(time.DateOnly, w.To)
	if err1 != nil || err2 != nil || t.Before(f) {
		return 0
	}
	return int(t.Sub(f).Hours()/24+0.5) + 1
}

// Sums maps a breakdown key ("" without breakdown) to a metric sum.
type Sums map[string]*big.Rat

// Total returns the sum over every key.
func (s Sums) Total() *big.Rat {
	t := new(big.Rat)
	for _, v := range s {
		t.Add(t, v)
	}
	return t
}

// SumMetric sums column metric over rows whose Date lies in w and whose
// match columns equal one of the accepted values (case-insensitive), keyed by
// column dim ("" for no breakdown). A missing column wraps ErrCorrupt.
func SumMetric(a *AnalyticsRows, metric string, match map[string][]string, dim string, w Window) (Sums, error) {
	out := Sums{}
	if a == nil {
		return out, nil
	}
	col := func(name string) (int, error) {
		i := a.Col(name)
		if i < 0 {
			return 0, fmt.Errorf("reports: %s has no column %q (columns: %s): %w", a.Report, name, strings.Join(a.Columns, ", "), ErrCorrupt)
		}
		return i, nil
	}
	di, err := col(ColDate)
	if err != nil {
		return nil, err
	}
	mi, err := col(metric)
	if err != nil {
		return nil, err
	}
	ki := -1
	if dim != "" {
		if ki, err = col(dim); err != nil {
			return nil, err
		}
	}
	type filter struct {
		i    int
		vals []string
	}
	var filters []filter
	for c, vals := range match {
		i, err := col(c)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter{i, vals})
	}
	cell := func(row []string, i int) string {
		if i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
rows:
	for line, row := range a.Rows {
		if !w.Contains(cell(row, di)) {
			continue
		}
		for _, f := range filters {
			v, ok := cell(row, f.i), false
			for _, want := range f.vals {
				ok = ok || strings.EqualFold(v, want)
			}
			if !ok {
				continue rows
			}
		}
		raw := cell(row, mi)
		if raw == "" {
			continue
		}
		n, ok := new(big.Rat).SetString(raw)
		if !ok {
			return nil, fmt.Errorf("reports: %s row %d: %s %q is not a number: %w", a.Report, line+1, metric, raw, ErrCorrupt)
		}
		key := ""
		if ki >= 0 {
			key = cell(row, ki)
		}
		if out[key] == nil {
			out[key] = new(big.Rat)
		}
		out[key].Add(out[key], n)
	}
	return out, nil
}

// FunnelStage is impressions → product page views → downloads with the two
// conversion rates (percent, "" when the denominator is zero).
type FunnelStage struct {
	Impressions  string `json:"impressions"`
	PageViews    string `json:"page_views"`
	Downloads    string `json:"downloads"`
	PageViewRate string `json:"page_view_rate,omitempty"`
	DownloadRate string `json:"download_rate,omitempty"`
}

// FunnelLine is one breakdown key of the funnel, with the previous period
// and per-stage change when compared.
type FunnelLine struct {
	Key      string            `json:"key,omitempty"`
	Current  FunnelStage       `json:"current"`
	Previous *FunnelStage      `json:"previous,omitempty"`
	Change   map[string]string `json:"change,omitempty"`
}

// FunnelSums are the per-key stage sums of one period.
type FunnelSums struct {
	Impressions, PageViews, Downloads Sums
}

func get(s Sums, k string) *big.Rat {
	if v := s[k]; v != nil {
		return v
	}
	return new(big.Rat)
}

// Stage builds the stage for key k ("" totals every key when total is true).
func (f FunnelSums) stage(k string, total bool) FunnelStage {
	imp, pv, dl := get(f.Impressions, k), get(f.PageViews, k), get(f.Downloads, k)
	if total {
		imp, pv, dl = f.Impressions.Total(), f.PageViews.Total(), f.Downloads.Total()
	}
	return FunnelStage{
		Impressions: FormatRat(imp), PageViews: FormatRat(pv), Downloads: FormatRat(dl),
		PageViewRate: Rate(pv, imp), DownloadRate: Rate(dl, pv),
	}
}

// Funnel returns the total line followed by up to top breakdown lines
// ordered by current impressions (then downloads) descending, then key. prev
// nil means no comparison.
func Funnel(cur FunnelSums, prev *FunnelSums, breakdown bool, top int) (total FunnelLine, lines []FunnelLine) {
	total = funnelLine("", cur, prev, true)
	if !breakdown {
		return total, nil
	}
	keys := map[string]bool{}
	for _, s := range []Sums{cur.Impressions, cur.PageViews, cur.Downloads} {
		for k := range s {
			keys[k] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if c := get(cur.Impressions, a).Cmp(get(cur.Impressions, b)); c != 0 {
			return c > 0
		}
		if c := get(cur.Downloads, a).Cmp(get(cur.Downloads, b)); c != 0 {
			return c > 0
		}
		return a < b
	})
	if top > 0 && len(ordered) > top {
		ordered = ordered[:top]
	}
	for _, k := range ordered {
		lines = append(lines, funnelLine(k, cur, prev, false))
	}
	return total, lines
}

func funnelLine(k string, cur FunnelSums, prev *FunnelSums, total bool) FunnelLine {
	l := FunnelLine{Key: k, Current: cur.stage(k, total)}
	if prev == nil {
		return l
	}
	p := prev.stage(k, total)
	l.Previous = &p
	l.Change = map[string]string{}
	for name, pair := range map[string][2]string{
		"impressions": {l.Current.Impressions, p.Impressions},
		"page_views":  {l.Current.PageViews, p.PageViews},
		"downloads":   {l.Current.Downloads, p.Downloads},
	} {
		c, _ := new(big.Rat).SetString(pair[0])
		pr, _ := new(big.Rat).SetString(pair[1])
		if s := Change(c, pr); s != "" {
			l.Change[name] = s
		}
	}
	if len(l.Change) == 0 {
		l.Change = nil
	}
	return l
}

// FormatRat formats r as an integer or a decimal with up to 6 places.
func FormatRat(r *big.Rat) string { return formatRat(r) }

// Rate returns num/den as a percentage with two decimals ("12.50%"), or ""
// when den is zero.
func Rate(num, den *big.Rat) string {
	if den == nil || den.Sign() == 0 {
		return ""
	}
	r := new(big.Rat).Quo(num, den)
	r.Mul(r, big.NewRat(100, 1))
	return r.FloatString(2) + "%"
}

// Ratio returns num/den with two decimals, or "" when den is zero.
func Ratio(num, den *big.Rat) string {
	if den == nil || den.Sign() == 0 {
		return ""
	}
	return new(big.Rat).Quo(num, den).FloatString(2)
}

// Change returns the signed change from prev to cur in percent with one
// decimal ("+12.5%"), or "" when prev is zero.
func Change(cur, prev *big.Rat) string {
	if prev == nil || prev.Sign() == 0 {
		return ""
	}
	if cur == nil {
		cur = new(big.Rat)
	}
	d := new(big.Rat).Sub(cur, prev)
	d.Quo(d, new(big.Rat).Abs(prev))
	d.Mul(d, big.NewRat(100, 1))
	s := d.FloatString(1)
	if d.Sign() > 0 {
		s = "+" + s
	}
	return s + "%"
}

// Coverage is how much of a window the data covers.
type Coverage struct {
	DaysExpected int      `json:"days_expected"`
	DaysPresent  int      `json:"days_present"`
	MissingDays  []string `json:"missing_days,omitempty"`
	LatestDay    string   `json:"latest_day,omitempty"`
	// LagDays is today minus the latest day with data (-1 when none).
	LagDays int `json:"lag_days"`
}

// CoverageOf reports which days of w have at least one row in any of sets,
// and the lag between today and the newest data day overall.
func CoverageOf(w Window, today time.Time, sets ...*AnalyticsRows) Coverage {
	present := map[string]bool{}
	latest := ""
	for _, a := range sets {
		if a == nil {
			continue
		}
		di := a.Col(ColDate)
		if di < 0 {
			continue
		}
		for _, row := range a.Rows {
			if di >= len(row) {
				continue
			}
			d := strings.TrimSpace(row[di])
			if w.Contains(d) {
				present[d] = true
			}
			if d > latest {
				latest = d
			}
		}
	}
	c := Coverage{DaysExpected: w.Days(), LatestDay: latest, LagDays: -1}
	if f, err := time.Parse(time.DateOnly, w.From); err == nil {
		for i := range c.DaysExpected {
			d := f.AddDate(0, 0, i).Format(time.DateOnly)
			if present[d] {
				c.DaysPresent++
			} else {
				c.MissingDays = append(c.MissingDays, d)
			}
		}
	}
	if l, err := time.Parse(time.DateOnly, latest); err == nil {
		t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
		c.LagDays = int(t.Sub(l).Hours()/24 + 0.5)
	}
	return c
}
