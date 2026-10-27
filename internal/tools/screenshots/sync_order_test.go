package screenshots

import (
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// readShots answers GET /v1/appScreenshots/{id} with fn's state; an empty
// state answers fx instead (an error fixture).
func readShots(t *testing.T, k *tooltest.Kit, f *fake, fn func(id string, n int) (string, tooltest.Fixture)) {
	t.Helper()
	calls := map[string]int{}
	k.ASC.HandleFunc(http.MethodGet, "/v1/appScreenshots/{id}", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := path.Base(req.Path)
		calls[id]++
		state, fx := fn(id, calls[id])
		if state == "" {
			return fx
		}
		s := f.shots[id]
		s.state = state
		return tooltest.Fixture{Status: 200, Body: doc(t, s.res("appScreenshots"))}
	})
}

// replaceOne seeds old.png and plans replacing it with a new 01.png.
func replaceOne(t *testing.T, k *tooltest.Kit, f *fake) (old, args string) {
	t.Helper()
	old = f.addShot("loc-en", "APP_IPHONE_67", "old.png", "deadbeef")
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, false, 1))
	return old, withPlan(t, k, `{"dir":"shots","mode":"replace"}`)
}

func itemFor(out syncOut, id string) (itemOut, bool) {
	for _, it := range out.Items {
		if it.ID == id {
			return it, true
		}
	}
	return itemOut{}, false
}

func hasNext(res *tool.Result, name string) bool {
	return slices.ContainsFunc(res.Next, func(n tool.NextStep) bool { return n.Tool == name })
}

// A replacement whose processing FAILED must not cost the old screenshot:
// its delete is held as not_attempted and a replan is suggested.
func TestSyncReplaceKeepsOldWhenReplacementFails(t *testing.T) {
	k, f := newKit(t)
	old, args := replaceOne(t, k, f)
	readShots(t, k, f, func(string, int) (string, tooltest.Fixture) { return "FAILED", tooltest.Fixture{} })

	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", args))
	out := tooltest.Decode[syncOut](t, res)
	for _, m := range mutations(k) {
		if strings.HasPrefix(m, "DELETE") {
			t.Fatalf("deleted although the replacement FAILED: %q", mutations(k))
		}
	}
	it, ok := itemFor(out, old)
	if !ok || it.Action != actionNotAttempted || it.State != stateAwaitingReplacement {
		t.Errorf("old item = %+v (found %v)", it, ok)
	}
	if out.Status != "partial" || !hasNext(res, "screenshots_plan") {
		t.Errorf("status %q next %+v", out.Status, res.Next)
	}
	f.mu.Lock()
	_, kept := f.shots[old]
	f.mu.Unlock()
	if !kept {
		t.Error("old screenshot gone")
	}
}

// Deletes of a set run only after its uploads reached COMPLETE.
func TestSyncReplaceDeletesAfterComplete(t *testing.T) {
	k, f := newKit(t)
	old, args := replaceOne(t, k, f)
	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", args))
	if out := tooltest.Decode[syncOut](t, res); out.Status != "synced" || out.Deleted != 1 {
		t.Fatalf("sync = %+v", out)
	}
	reqs := k.ASC.Requests()
	read := slices.IndexFunc(reqs, func(r tool.Request) bool {
		return r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/v1/appScreenshots/")
	})
	del := slices.IndexFunc(reqs, func(r tool.Request) bool { return r.Method == http.MethodDelete })
	if read < 0 || del < read || reqs[del].Path != "/v1/appScreenshots/"+old {
		t.Errorf("processing read at %d, delete at %d", read, del)
	}
}

// A read error while waiting keeps the batch result: uploads stay listed with
// state unknown, old screenshots stay, and screenshots_list is suggested.
func TestSyncWaitReadErrorKeepsBatch(t *testing.T) {
	k, f := newKit(t)
	old, args := replaceOne(t, k, f)
	readShots(t, k, f, func(string, int) (string, tooltest.Fixture) {
		return "", tooltest.Fixture{Status: 500, Body: []byte(`{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"boom"}]}`)}
	})
	res := call(t, k, "screenshots_sync", args)
	if res.IsError {
		t.Fatalf("result is an error: %+v", res.Data)
	}
	out, status := syncResult(t, res)
	if status != "still_processing" || out.Failed != 0 || out.Held != 1 {
		t.Errorf("status %q out = %+v", status, out)
	}
	var up itemOut
	for _, it := range out.Items {
		if it.Action == actionUploaded {
			up = it
		}
	}
	if up.ID == "" || up.State != stateUnknown || out.Uploaded != 1 || out.Pending != 1 {
		t.Errorf("out = %+v", out)
	}
	if it, _ := itemFor(out, old); it.Action != actionNotAttempted {
		t.Errorf("old item = %+v", it)
	}
	if !hasNext(res, "screenshots_list") || !slices.ContainsFunc(res.Meta.Warnings, func(w string) bool { return strings.Contains(w, "unknown") }) {
		t.Errorf("next %+v warnings %q", res.Next, res.Meta.Warnings)
	}
}

// A 404 right after create is eventual consistency: keep waiting.
func TestSyncWait404IsStillProcessing(t *testing.T) {
	k, f := newKit(t)
	old, args := replaceOne(t, k, f)
	readShots(t, k, f, func(_ string, n int) (string, tooltest.Fixture) {
		if n == 1 {
			return "", tooltest.Fixture{Status: 404, Body: []byte(`{"errors":[{"status":"404","code":"NOT_FOUND","title":"nope"}]}`)}
		}
		return "COMPLETE", tooltest.Fixture{}
	})
	out := tooltest.Decode[syncOut](t, tooltest.MustOK(t, call(t, k, "screenshots_sync", args)))
	if out.Status != "synced" || out.Deleted != 1 {
		t.Errorf("out = %+v", out)
	}
	if it, _ := itemFor(out, old); it.Action != actionDeleted {
		t.Errorf("old item = %+v", it)
	}
}

// A full set deletes only the overflow before uploading; the other deletes
// follow the uploads, and the preview states that order.
func TestSyncFullSetDeletesOnlyOverflowFirst(t *testing.T) {
	k, f := newKit(t)
	for i := 1; i <= 10; i++ {
		img := pngImage(t, 1290, 2796, false, uint8(i))
		f.addShot("loc-en", "APP_IPHONE_67", fmt.Sprintf("r%02d.png", i), md5Of(t, img))
		if i <= 6 {
			write(t, k, fmt.Sprintf("shots/en-US/%02d.png", i), img)
		}
	}
	write(t, k, "shots/en-US/07.png", pngImage(t, 1290, 2796, false, 40))
	write(t, k, "shots/en-US/08.png", pngImage(t, 1290, 2796, false, 41))
	args := withPlan(t, k, `{"dir":"shots","mode":"replace"}`)

	irr := k.Plans.Proposed[len(k.Plans.Proposed)-1].Irreversible
	first := 0
	for _, s := range irr {
		if strings.Contains(s, "first, before uploading") {
			first++
		}
	}
	if len(irr) != 4 || first != 2 {
		t.Errorf("preview = %q", irr)
	}

	out := tooltest.Decode[syncOut](t, tooltest.MustOK(t, call(t, k, "screenshots_sync", args)))
	if out.Status != "synced" || out.Deleted != 4 || out.Uploaded != 2 {
		t.Fatalf("out = %+v", out)
	}
	var kinds []string
	for _, m := range mutations(k) {
		kinds = append(kinds, strings.Fields(m)[0])
	}
	want := []string{"DELETE", "DELETE", "POST", "PATCH", "POST", "PATCH", "DELETE", "DELETE"}
	if !slices.Equal(kinds, want) {
		t.Errorf("mutations = %q", mutations(k))
	}
}
