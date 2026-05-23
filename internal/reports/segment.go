package reports

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// MaxRawRows caps the raw rows a segment summary may include.
const MaxRawRows = 500

// SegmentGroup is the metric sums for one combination of dimension values.
type SegmentGroup struct {
	Key     map[string]string `json:"key"`
	Metrics map[string]string `json:"metrics"`
	Rows    int               `json:"rows"`
	sums    []*big.Rat
}

// SegmentSummary summarizes one analytics report segment.
type SegmentSummary struct {
	Dimensions []string          `json:"dimensions"`
	Metrics    []string          `json:"metrics"`
	Totals     map[string]string `json:"totals"`
	Groups     []SegmentGroup    `json:"groups"`
	Omitted    int               `json:"omitted,omitempty"`
	RowCount   int               `json:"row_count"`
	RawColumns []string          `json:"raw_columns,omitempty"`
	RawRows    [][]string        `json:"raw_rows,omitempty"`
}

func formatRat(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	return strings.TrimRight(strings.TrimRight(r.FloatString(6), "0"), ".")
}

// SummarizeSegment sums the metric columns of an analytics segment table by
// the dimension columns. Groups are ordered by the first metric descending,
// then key; topN > 0 keeps the first topN. rawRows > 0 includes up to
// min(rawRows, MaxRawRows) raw rows. Unknown columns or non-numeric metric
// cells return an error wrapping ErrCorrupt.
func SummarizeSegment(t *Table, dims, metrics []string, topN, rawRows int) (*SegmentSummary, error) {
	if len(metrics) == 0 {
		return nil, fmt.Errorf("reports: segment summary needs at least one metric: %w", ErrCorrupt)
	}
	idx := func(names []string) ([]int, error) {
		out := make([]int, len(names))
		for i, n := range names {
			if out[i] = t.Col(n); out[i] < 0 {
				return nil, fmt.Errorf("reports: segment has no column %q (have %s): %w", n, strings.Join(t.Columns, ", "), ErrCorrupt)
			}
		}
		return out, nil
	}
	di, err := idx(dims)
	if err != nil {
		return nil, err
	}
	mi, err := idx(metrics)
	if err != nil {
		return nil, err
	}
	totals := make([]*big.Rat, len(metrics))
	for i := range totals {
		totals[i] = new(big.Rat)
	}
	groups := map[string]*SegmentGroup{}
	for line, row := range t.Rows {
		parts := make([]string, len(di))
		for i, c := range di {
			parts[i] = row[c]
		}
		id := strings.Join(parts, "\x00")
		g, ok := groups[id]
		if !ok {
			g = &SegmentGroup{Key: make(map[string]string, len(dims)), sums: make([]*big.Rat, len(metrics))}
			for i, d := range dims {
				g.Key[d] = parts[i]
			}
			for i := range g.sums {
				g.sums[i] = new(big.Rat)
			}
			groups[id] = g
		}
		g.Rows++
		for i, name := range metrics {
			v, err := ParseAmount(row[mi[i]])
			if err != nil {
				return nil, fmt.Errorf("reports: segment row %d metric %q: %w", line+1, name, err)
			}
			g.sums[i].Add(g.sums[i], v)
			totals[i].Add(totals[i], v)
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if c := groups[ids[i]].sums[0].Cmp(groups[ids[j]].sums[0]); c != 0 {
			return c > 0
		}
		return ids[i] < ids[j]
	})
	s := &SegmentSummary{Dimensions: dims, Metrics: metrics, Totals: map[string]string{}, RowCount: len(t.Rows)}
	for i, m := range metrics {
		s.Totals[m] = formatRat(totals[i])
	}
	for i, id := range ids {
		if topN > 0 && i >= topN {
			s.Omitted = len(ids) - topN
			break
		}
		g := groups[id]
		g.Metrics = make(map[string]string, len(metrics))
		for j, m := range metrics {
			g.Metrics[m] = formatRat(g.sums[j])
		}
		s.Groups = append(s.Groups, *g)
	}
	if rawRows > 0 {
		n := min(rawRows, MaxRawRows, len(t.Rows))
		s.RawColumns = t.Columns
		s.RawRows = t.Rows[:n]
	}
	return s, nil
}
