package tool

import (
	"encoding/json"
	"testing"
)

func TestPagingTotal(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want int
	}{
		{"absent", nil, 0},
		{"no total", map[string]any{"paging": map[string]any{"limit": 50.0}}, 0},
		{"float64", map[string]any{"paging": map[string]any{"total": 42.0}}, 42},
		{"int", map[string]any{"paging": map[string]any{"total": 7}}, 7},
		{"json.Number", map[string]any{"paging": map[string]any{"total": json.Number("1234")}}, 1234},
		{"wrong type", map[string]any{"paging": map[string]any{"total": "9"}}, 0},
	}
	for _, tt := range tests {
		if got := PagingTotal(tt.meta); got != tt.want {
			t.Errorf("%s: PagingTotal = %d, want %d", tt.name, got, tt.want)
		}
		if got := (&Document{Meta: tt.meta}).Total(); got != tt.want {
			t.Errorf("%s: Document.Total = %d, want %d", tt.name, got, tt.want)
		}
		if got := (&Response{Meta: tt.meta}).Total(); got != tt.want {
			t.Errorf("%s: Response.Total = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestIndexIncludedAndRelData(t *testing.T) {
	inc := IndexIncluded([]Resource{{ID: "1", Type: "builds"}, {ID: "1", Type: "apps"}})
	if len(inc) != 2 || inc["builds/1"].Type != "builds" || inc["apps/1"].Type != "apps" {
		t.Fatalf("IndexIncluded = %v", inc)
	}
	b, err := json.Marshal(RelData("apps", "9"))
	if err != nil || string(b) != `{"data":{"id":"9","type":"apps"}}` {
		t.Fatalf("RelData = %s, %v", b, err)
	}
}

func TestIsDigits(t *testing.T) {
	for s, want := range map[string]bool{"": false, "0": true, "1234567890": true, "12a": false, "-1": false, "١٢": false} {
		if got := IsDigits(s); got != want {
			t.Errorf("IsDigits(%q) = %v, want %v", s, got, want)
		}
	}
}
