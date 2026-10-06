package tooltest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// PlannerConformance checks that a [tool.Planner] has the contract every
// plan-confirm tool relies on. newPlanner must return a fresh planner reading
// time from now; ttl is the planner's plan lifetime and must equal
// [tool.PlanTTL]. Run it against every implementation (FakePlanner and the
// server's Planner) so tool tests and production agree.
func PlannerConformance(t *testing.T, ttl time.Duration, newPlanner func(now func() time.Time) tool.Planner) {
	t.Helper()
	if ttl != tool.PlanTTL {
		t.Fatalf("planner TTL %v differs from tool.PlanTTL %v", ttl, tool.PlanTTL)
	}
	ctx := context.Background()
	args := map[string]any{"app": "1", "plan_id": "ignored"}
	type setup struct {
		p     tool.Planner
		clock *clock
	}
	fresh := func() setup {
		c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
		return setup{p: newPlanner(c.now), clock: c}
	}
	propose := func(t *testing.T, s setup) string {
		t.Helper()
		id, err := s.p.Propose(ctx, "x_apply", args, "fp1", tool.PlanPreview{Summary: "s"})
		if err != nil || id == "" {
			t.Fatalf("Propose = %q, %v", id, err)
		}
		return id
	}
	stale := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, tool.ErrStalePlan) {
			t.Fatalf("%s: err = %v, want stale_plan", what, err)
		}
	}

	t.Run("verify_once", func(t *testing.T) {
		s := fresh()
		id := propose(t, s)
		// confirm/plan_id keys are not part of the binding.
		if err := s.p.Verify(ctx, id, "x_apply", map[string]any{"app": "1", "plan_id": id}, "fp1"); err != nil {
			t.Fatalf("Verify = %v", err)
		}
		stale(t, s.p.Verify(ctx, id, "x_apply", args, "fp1"), "second verify")
	})
	t.Run("unknown_id", func(t *testing.T) {
		s := fresh()
		stale(t, s.p.Verify(ctx, "nope", "x_apply", args, "fp1"), "unknown id")
	})
	mismatches := []struct {
		name, tool, fp string
		args           any
	}{
		{"args_mismatch_consumes", "x_apply", "fp1", map[string]any{"app": "2"}},
		{"tool_mismatch_consumes", "y_apply", "fp1", args},
		{"fingerprint_mismatch_consumes", "x_apply", "fp2", args},
		{"empty_fingerprint_consumes", "x_apply", "", args},
	}
	for _, m := range mismatches {
		t.Run(m.name, func(t *testing.T) {
			s := fresh()
			id := propose(t, s)
			stale(t, s.p.Verify(ctx, id, m.tool, m.args, m.fp), "mismatch")
			stale(t, s.p.Verify(ctx, id, "x_apply", args, "fp1"), "retry with correct args")
		})
	}
	t.Run("valid_just_before_ttl", func(t *testing.T) {
		s := fresh()
		id := propose(t, s)
		s.clock.add(ttl - time.Nanosecond)
		if err := s.p.Verify(ctx, id, "x_apply", args, "fp1"); err != nil {
			t.Fatalf("Verify at TTL-1ns = %v", err)
		}
	})
	t.Run("expired_at_ttl", func(t *testing.T) {
		s := fresh()
		id := propose(t, s)
		s.clock.add(ttl)
		stale(t, s.p.Verify(ctx, id, "x_apply", args, "fp1"), "verify at TTL")
	})
	t.Run("empty_fingerprint_refused", func(t *testing.T) {
		s := fresh()
		if id, err := s.p.Propose(ctx, "x_apply", args, "", tool.PlanPreview{}); err == nil || !errors.Is(err, tool.ErrInternal) {
			t.Fatalf("Propose with empty fingerprint = %q, %v", id, err)
		}
	})
	t.Run("ids_unique", func(t *testing.T) {
		s := fresh()
		a, b := propose(t, s), propose(t, s)
		if a == b {
			t.Fatal("duplicate plan ids")
		}
	})
	claim := func(t *testing.T, s setup, a any) string {
		t.Helper()
		id, err := s.p.Propose(ctx, "x_delete", a, tool.FlagFingerprint, tool.PlanPreview{Summary: "s"})
		if err != nil || id == "" {
			t.Fatalf("Propose = %q, %v", id, err)
		}
		return id
	}
	needPreview := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, tool.ErrPreviewRequired) {
			t.Fatalf("%s: err = %v, want preview_required", what, err)
		}
	}
	t.Run("claim_once", func(t *testing.T) {
		s := fresh()
		claim(t, s, map[string]any{"app": "1"})
		// confirm is not part of the binding.
		if err := s.p.Claim(ctx, "x_delete", map[string]any{"app": "1", "confirm": true}); err != nil {
			t.Fatalf("Claim = %v", err)
		}
		needPreview(t, s.p.Claim(ctx, "x_delete", map[string]any{"app": "1"}), "second claim")
	})
	t.Run("claim_without_preview", func(t *testing.T) {
		needPreview(t, fresh().p.Claim(ctx, "x_delete", args), "no preview")
	})
	t.Run("claim_tool_mismatch", func(t *testing.T) {
		s := fresh()
		claim(t, s, args)
		needPreview(t, s.p.Claim(ctx, "y_delete", args), "other tool")
		if err := s.p.Claim(ctx, "x_delete", args); err != nil {
			t.Fatalf("mismatch consumed the plan: %v", err)
		}
	})
	t.Run("claim_args_mismatch", func(t *testing.T) {
		s := fresh()
		claim(t, s, args)
		needPreview(t, s.p.Claim(ctx, "x_delete", map[string]any{"app": "2"}), "other args")
	})
	t.Run("claim_ignores_plan_fingerprints", func(t *testing.T) {
		s := fresh()
		if _, err := s.p.Propose(ctx, "x_delete", args, "fp1", tool.PlanPreview{}); err != nil {
			t.Fatal(err)
		}
		needPreview(t, s.p.Claim(ctx, "x_delete", args), "plan_id plan")
	})
	t.Run("claim_valid_just_before_ttl", func(t *testing.T) {
		s := fresh()
		claim(t, s, args)
		s.clock.add(ttl - time.Nanosecond)
		if err := s.p.Claim(ctx, "x_delete", args); err != nil {
			t.Fatalf("Claim at TTL-1ns = %v", err)
		}
	})
	t.Run("claim_expired_at_ttl", func(t *testing.T) {
		s := fresh()
		claim(t, s, args)
		s.clock.add(ttl)
		needPreview(t, s.p.Claim(ctx, "x_delete", args), "claim at TTL")
	})
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
