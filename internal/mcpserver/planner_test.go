package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

func TestPlanner(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	p := NewPlanner(func() time.Time { return now })
	args := map[string]any{"a": 1, "confirm": "ignored"}

	if _, err := p.Propose(ctx, "t", args, "", tool.PlanPreview{}); err == nil {
		t.Fatal("empty fingerprint accepted")
	}
	tests := []struct {
		name    string
		tool    string
		args    any
		fp      string
		advance time.Duration
		ok      bool
	}{
		{"match", "t", map[string]any{"a": 1}, "fp", 0, true},
		{"other tool", "u", args, "fp", 0, false},
		{"other args", "t", map[string]any{"a": 2}, "fp", 0, false},
		{"state changed", "t", args, "fp2", 0, false},
		{"expired", "t", args, "fp", PlanTTL, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := p.Propose(ctx, "t", args, "fp", tool.PlanPreview{})
			if err != nil || len(id) != 32 {
				t.Fatalf("propose = %q, %v", id, err)
			}
			now = now.Add(tt.advance)
			err = p.Verify(ctx, id, tt.tool, tt.args, tt.fp)
			if tt.ok != (err == nil) {
				t.Fatalf("verify err = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, tool.ErrStalePlan) {
				t.Fatalf("error %v does not wrap ErrStalePlan", err)
			}
			if err := p.Verify(ctx, id, "t", args, "fp"); !errors.Is(err, tool.ErrStalePlan) {
				t.Fatalf("plan reusable: %v", err)
			}
		})
	}
}

func TestPlannerEvictsOldest(t *testing.T) {
	ctx := context.Background()
	p := NewPlanner(nil)
	first, err := p.Propose(ctx, "t", 0, "fp", tool.PlanPreview{})
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 1; i <= PlanCapacity; i++ {
		if last, err = p.Propose(ctx, "t", i, "fp", tool.PlanPreview{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Verify(ctx, first, "t", 0, "fp"); err == nil {
		t.Fatal("oldest plan survived eviction")
	}
	if err := p.Verify(ctx, last, "t", PlanCapacity, "fp"); err != nil {
		t.Fatalf("newest plan lost: %v", err)
	}
}
