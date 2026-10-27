package screenshots

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func setState(f *fake, id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shots[id].state = state
}

// syncResult decodes a sync result, unwrapping a still_processing one.
func syncResult(t *testing.T, res *tool.Result) (syncOut, string) {
	t.Helper()
	w := tooltest.Decode[struct {
		Status string  `json:"status"`
		Result syncOut `json:"result"`
	}](t, res)
	if w.Status == "still_processing" {
		return w.Result, w.Status
	}
	out := tooltest.Decode[syncOut](t, res)
	return out, out.Status
}

// A FAILED remote with the local file's checksum is not a match: the file is
// uploaded again and, in replace mode, the FAILED one is deleted.
func TestPlanReuploadsFailedRemote(t *testing.T) {
	k, f := newKit(t)
	a := pngImage(t, 1290, 2796, false, 1)
	write(t, k, "shots/en-US/01.png", a)
	old := f.addShot("loc-en", "APP_IPHONE_67", "old.png", "deadbeef")
	failed := f.addShot("loc-en", "APP_IPHONE_67", "01.png", md5Of(t, a))
	setState(f, failed, "FAILED")

	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots","mode":"replace"}`)))
	s := p.Sets[0]
	if len(s.Skips) != 0 || len(s.Uploads) != 1 || len(s.Waiting) != 0 {
		t.Errorf("skips/uploads/waiting = %+v / %+v / %+v", s.Skips, s.Uploads, s.Waiting)
	}
	var del []string
	for _, d := range s.Deletes {
		del = append(del, d.ID)
	}
	if len(del) != 2 || !strings.Contains(strings.Join(del, ","), old) || !strings.Contains(strings.Join(del, ","), failed) {
		t.Errorf("deletes = %v", del)
	}

	// Add mode re-uploads too and deletes nothing.
	p = tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	if s := p.Sets[0]; len(s.Uploads) != 1 || len(s.Deletes) != 0 {
		t.Errorf("add mode set = %+v", s)
	}
}

// A kept screenshot still processing holds the set's deletes: the replan
// never deletes the old good screenshot.
func TestPlanHoldsDeletesWhileKeptShotProcesses(t *testing.T) {
	k, f := newKit(t)
	a := pngImage(t, 1290, 2796, false, 1)
	write(t, k, "shots/en-US/01.png", a)
	old := f.addShot("loc-en", "APP_IPHONE_67", "old.png", "deadbeef")
	fresh := f.addShot("loc-en", "APP_IPHONE_67", "01.png", md5Of(t, a))
	setState(f, fresh, "UPLOAD_COMPLETE")

	res := tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots","mode":"replace"}`))
	p := tooltest.Decode[planResult](t, res)
	s := p.Sets[0]
	if len(s.Skips) != 1 || s.Skips[0].ID != fresh || len(s.Uploads) != 0 || len(s.Deletes) != 0 || s.Reorder {
		t.Errorf("set = %+v", s)
	}
	if len(s.Waiting) != 1 || s.Waiting[0].ID != old || p.Summary.Waiting != 1 {
		t.Errorf("waiting = %+v summary %+v", s.Waiting, p.Summary)
	}
	if p.Status != statusNoChanges || !hasNext(res, "screenshots_list") ||
		!strings.Contains(strings.Join(res.Meta.Warnings, " "), "still processing") {
		t.Errorf("status %q next %+v warnings %q", p.Status, res.Next, res.Meta.Warnings)
	}

	// Once it is COMPLETE the old one is deleted.
	setState(f, fresh, "COMPLETE")
	p = tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots","mode":"replace"}`)))
	if s := p.Sets[0]; len(s.Deletes) != 1 || s.Deletes[0].ID != old || len(s.Waiting) != 0 {
		t.Errorf("after COMPLETE set = %+v", s)
	}
}

// Deletes held behind a replacement that is merely still processing are not
// failures: the result is still-processing with screenshots_list next.
func TestSyncHeldDeletesAreNotFailures(t *testing.T) {
	k, f := newKit(t)
	old, args := replaceOne(t, k, f)
	readShots(t, k, f, func(string, int) (string, tooltest.Fixture) { return "UPLOAD_COMPLETE", tooltest.Fixture{} })

	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", args))
	out, status := syncResult(t, res)
	if out.Failed != 0 || out.Held != 1 || out.Pending != 1 || out.Status != "uploaded" || status != "still_processing" {
		t.Errorf("status %q out = %+v", status, out)
	}
	if it, _ := itemFor(out, old); it.State != stateAwaitingReplacement {
		t.Errorf("old item = %+v", it)
	}
	if !hasNext(res, "screenshots_list") || hasNext(res, "screenshots_plan") {
		t.Errorf("next = %+v", res.Next)
	}
	if strings.Contains(strings.Join(res.Meta.Warnings, " "), "failed") {
		t.Errorf("warnings = %q", res.Meta.Warnings)
	}
}

// With no editable version screenshots_list says it shows the live one and
// points at release_prepare.
func TestListSaysLiveFallback(t *testing.T) {
	k, f := newKit(t)
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{tool.VersionLive: {ID: "ver0", String: "1.3", State: "READY_FOR_DISTRIBUTION"}}
	f.addShot("loc-en", "APP_IPHONE_67", "01.png", "abc")
	res := tooltest.MustOK(t, call(t, k, "screenshots_list", `{}`))
	out := tooltest.Decode[listOut](t, res)
	if out.VersionSource != "live_fallback" || !strings.Contains(out.Note, "release_prepare") || out.Version.State != "READY_FOR_DISTRIBUTION" {
		t.Errorf("out = %+v", out)
	}
	if !hasNext(res, "release_prepare") || !strings.Contains(strings.Join(res.Meta.Warnings, " "), "no editable version") {
		t.Errorf("next %+v warnings %q", res.Next, res.Meta.Warnings)
	}

	// An editable version gets no note.
	k2, f2 := newKit(t)
	f2.addShot("loc-en", "APP_IPHONE_67", "01.png", "abc")
	res = tooltest.MustOK(t, call(t, k2, "screenshots_list", `{}`))
	if out := tooltest.Decode[listOut](t, res); out.VersionSource != "" || out.Note != "" || hasNext(res, "release_prepare") {
		t.Errorf("editable out = %+v next %+v", out, res.Next)
	}
}
