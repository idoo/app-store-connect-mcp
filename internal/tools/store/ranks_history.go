package store

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"slices"
	"strconv"
	"time"
)

// asoKind is the state kind of rank histories; the key is the numeric App
// Store trackId. The profile is not part of the key: the data is public.
const asoKind = "aso"

const (
	historyKeep      = 365 * 24 * time.Hour
	historyMaxPoints = 20000
	sparkMax         = 30
)

// asoPair is one tracked keyword × storefront.
type asoPair struct {
	K string `json:"k"`
	C string `json:"c"`
}

// asoPoint is one measurement: R is the 1-based rank, 0 when the app was not
// in the top D results.
type asoPoint struct {
	T int64  `json:"t"`
	K string `json:"k"`
	C string `json:"c"`
	R int    `json:"r,omitempty"`
	D int    `json:"d"`
}

// asoHistory is the persisted record aso/<trackId>.
type asoHistory struct {
	Tracked []asoPair  `json:"tracked,omitempty"`
	Points  []asoPoint `json:"points,omitempty"`
}

// track adds pairs to the tracked set, keeping its order and uniqueness.
func (h *asoHistory) track(pairs []asoPair) {
	for _, p := range pairs {
		if !slices.Contains(h.Tracked, p) {
			h.Tracked = append(h.Tracked, p)
		}
	}
}

// untrack removes tracked pairs matching keyword (and country unless empty);
// history points stay. It returns how many pairs were removed.
func (h *asoHistory) untrack(keywords, countries []string) int {
	n := len(h.Tracked)
	h.Tracked = slices.DeleteFunc(h.Tracked, func(p asoPair) bool {
		return slices.Contains(keywords, p.K) && (len(countries) == 0 || slices.Contains(countries, p.C))
	})
	return n - len(h.Tracked)
}

// add appends points (a measurement on the same UTC day for the same pair
// replaces the earlier one), then drops points older than a year and keeps
// at most historyMaxPoints, newest first.
func (h *asoHistory) add(now time.Time, pts []asoPoint) {
	for _, p := range pts {
		day := dayOf(p.T)
		h.Points = slices.DeleteFunc(h.Points, func(q asoPoint) bool {
			return q.K == p.K && q.C == p.C && dayOf(q.T) == day
		})
		h.Points = append(h.Points, p)
	}
	h.prune(now)
}

func (h *asoHistory) prune(now time.Time) {
	cut := now.Add(-historyKeep).Unix()
	h.Points = slices.DeleteFunc(h.Points, func(q asoPoint) bool { return q.T < cut })
	slices.SortStableFunc(h.Points, func(a, b asoPoint) int { return cmp.Compare(a.T, b.T) })
	if n := len(h.Points); n > historyMaxPoints {
		h.Points = slices.Delete(h.Points, 0, n-historyMaxPoints)
	}
}

func dayOf(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.DateOnly) }

// series returns the pair's points in time order.
func (h *asoHistory) series(p asoPair) []asoPoint {
	var out []asoPoint
	for _, q := range h.Points {
		if q.K == p.K && q.C == p.C {
			out = append(out, q)
		}
	}
	return out
}

// pairs lists the tracked pairs followed by untracked pairs with history,
// in first-seen order.
func (h *asoHistory) pairs() []asoPair {
	out := slices.Clone(h.Tracked)
	for _, q := range h.Points {
		if p := (asoPair{q.K, q.C}); !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// rankSummary is one keyword × storefront row of the report.
type rankSummary struct {
	Keyword string `json:"keyword"`
	Country string `json:"country"`
	// Rank is the latest 1-based rank; null means not in the top Depth.
	Rank  *int   `json:"rank"`
	Depth int    `json:"depth,omitempty"`
	At    string `json:"at,omitempty"`
	// Previous is the rank of the measurement before the latest.
	Previous *int `json:"previous,omitempty"`
	// Change7d and Change30d are positions gained (positive = moved up)
	// against the latest measurement at least 7 / 30 days older.
	Change7d  *int   `json:"change_7d,omitempty"`
	Change30d *int   `json:"change_30d,omitempty"`
	Best      *int   `json:"best,omitempty"`
	Worst     *int   `json:"worst,omitempty"`
	Trend     string `json:"trend,omitempty"`
	Spark     string `json:"spark,omitempty"`
	Points    int    `json:"points"`
	Tracked   bool   `json:"tracked"`
	// Above lists up to 3 competitors ranked above the app in this run.
	Above []competitor `json:"above,omitempty"`
	Error string       `json:"error,omitempty"`
}

// summarize builds the row for p over the last days (best, worst, spark)
// while change, previous and trend use the whole history.
func summarize(h *asoHistory, p asoPair, now time.Time, days int) rankSummary {
	s := rankSummary{Keyword: p.K, Country: p.C, Tracked: slices.Contains(h.Tracked, p)}
	all := h.series(p)
	if len(all) == 0 {
		s.Trend = "no_data"
		return s
	}
	last := all[len(all)-1]
	s.Rank, s.Depth, s.At = rankPtr(last.R), last.D, time.Unix(last.T, 0).UTC().Format(time.RFC3339)
	s.Trend = "new"
	if len(all) > 1 {
		prev := all[len(all)-2]
		s.Previous = rankPtr(prev.R)
		s.Trend = trend(prev.R, last.R)
	}
	s.Change7d = change(all, last, 7)
	s.Change30d = change(all, last, 30)
	cut := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	var window []asoPoint
	for _, q := range all {
		if q.T >= cut {
			window = append(window, q)
		}
	}
	s.Points = len(window)
	for _, q := range window {
		if q.R == 0 {
			continue
		}
		if s.Best == nil || q.R < *s.Best {
			s.Best = rankPtr(q.R)
		}
		if s.Worst == nil || q.R > *s.Worst {
			s.Worst = rankPtr(q.R)
		}
	}
	s.Spark = spark(window)
	return s
}

func rankPtr(r int) *int {
	if r == 0 {
		return nil
	}
	return &r
}

// trend compares two consecutive ranks (0 = unranked).
func trend(prev, cur int) string {
	switch {
	case prev == 0 && cur == 0:
		return "not_ranked"
	case prev == 0:
		return "entered"
	case cur == 0:
		return "dropped_out"
	case cur < prev:
		return "up"
	case cur > prev:
		return "down"
	}
	return "same"
}

// change returns positions gained since the latest point at least days older
// than last; nil when there is no such point or either side is unranked.
func change(all []asoPoint, last asoPoint, days int) *int {
	cut := last.T - int64(days)*24*3600
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].T <= cut {
			if all[i].R == 0 || last.R == 0 {
				return nil
			}
			d := all[i].R - last.R
			return &d
		}
	}
	return nil
}

// spark renders the last sparkMax ranks: a taller bar is a better rank
// relative to the depth, and · is "not in the top depth".
func spark(pts []asoPoint) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	if len(pts) > sparkMax {
		pts = pts[len(pts)-sparkMax:]
	}
	var b []rune
	for _, q := range pts {
		if q.R == 0 || q.D <= 0 {
			b = append(b, '·')
			continue
		}
		i := (q.D - q.R) * len(bars) / q.D
		b = append(b, bars[min(max(i, 0), len(bars)-1)])
	}
	return string(b)
}

// historyCSV renders the full history as CSV (date, keyword, country, rank,
// depth); an empty rank means not in the top depth.
func historyCSV(h *asoHistory) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"time", "keyword", "country", "rank", "depth"})
	for _, q := range h.Points {
		r := ""
		if q.R > 0 {
			r = strconv.Itoa(q.R)
		}
		_ = w.Write([]string{time.Unix(q.T, 0).UTC().Format(time.RFC3339), q.K, q.C, r, strconv.Itoa(q.D)})
	}
	w.Flush()
	return buf.Bytes()
}
