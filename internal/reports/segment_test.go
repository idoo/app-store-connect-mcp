package reports

import (
	"errors"
	"strings"
	"testing"
)

func TestSummarizeSegmentGolden(t *testing.T) {
	tab := openFixture(t, "analytics_segment.csv")
	s, err := SummarizeSegment(tab, []string{"source type"}, []string{"Counts", "Unique Counts"}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals["Counts"] != "288" || s.Omitted != 1 || s.Groups[0].Key["source type"] != "App Store search" ||
		s.Groups[0].Metrics["Counts"] != "245" || s.RawRows != nil {
		t.Fatalf("summary = %+v", s)
	}
	golden(t, "analytics_by_source", s)
}

func TestSummarizeSegmentRawRowsAndDecimals(t *testing.T) {
	tab, err := Decode(strings.NewReader("Territory,Value\nUS,0.1\nUS,0.2\nDE,1\n"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := SummarizeSegment(tab, []string{"Territory"}, []string{"Value"}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.Groups[0].Key["Territory"] != "DE" || s.Groups[1].Metrics["Value"] != "0.3" || len(s.RawRows) != 2 {
		t.Fatalf("summary = %+v", s)
	}
	all, err := SummarizeSegment(tab, nil, []string{"Value"}, 0, 10_000)
	if err != nil || len(all.Groups) != 1 || all.Totals["Value"] != "1.3" || len(all.RawRows) != 3 {
		t.Fatalf("summary = %+v, err = %v", all, err)
	}
}

func TestSummarizeSegmentErrors(t *testing.T) {
	tab, err := Decode(strings.NewReader("Territory,Value\nUS,abc\n"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		dims, metrics []string
	}{
		{"no metrics", []string{"Territory"}, nil},
		{"unknown dim", []string{"Nope"}, []string{"Value"}},
		{"unknown metric", nil, []string{"Nope"}},
		{"non numeric", nil, []string{"Value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SummarizeSegment(tab, tt.dims, tt.metrics, 0, 0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}
