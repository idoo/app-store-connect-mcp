package testflight

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A device/platform/build filter hides other feedback, so mark_seen keeps the
// app-wide cursor and warns (HCS-2).
func TestFeedbackFilteredMarkSeenKeepsCursor(t *testing.T) {
	k := feedbackKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_feedback_list"), k.Env,
		`{"kind":"crashes","device_model":"iPhone15,2","mark_seen":true}`))
	if c, _ := k.State.Cursor(k.Env.Profile.Name, "app1", kindCrashes.cursorName()); c != "" {
		t.Fatalf("cursor moved to %q", c)
	}
	if !strings.Contains(strings.Join(res.Meta.Warnings, "|"), "last_seen was kept") {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
}

// An explicit older since with mark_seen never moves the cursor backwards
// (HCS-3).
func TestFeedbackCursorNeverMovesBackwards(t *testing.T) {
	k := feedbackKit(t)
	const later = "2026-02-01T00:00:00Z"
	if err := k.State.SetCursor(k.Env.Profile.Name, "app1", kindCrashes.cursorName(), later); err != nil {
		t.Fatal(err)
	}
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_feedback_list"), k.Env,
		`{"kind":"crashes","since":"2025-01-01","mark_seen":true}`))
	if c, _ := k.State.Cursor(k.Env.Profile.Name, "app1", kindCrashes.cursorName()); c != later {
		t.Fatalf("cursor = %q, want %q", c, later)
	}
	if d := tooltest.Decode[feedbackDecoded](t, res); d.Cursors["crashes"].AdvancedTo != later {
		t.Fatalf("cursors = %s", tooltest.JSON(t, d.Cursors))
	}
}
