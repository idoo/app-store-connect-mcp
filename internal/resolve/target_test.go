package resolve

import (
	"context"
	"errors"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// idApps resolves every app reference to itself so Target tests exercise
// only version resolution.
type idApps struct{ *Resolver }

func (idApps) App(_ context.Context, ref tool.AppRef) (tool.App, error) {
	return tool.App{ID: string(ref), Name: string(ref)}, nil
}

func TestTargetFallbacks(t *testing.T) {
	errNoEdit := errors.New("no editable version")
	tests := []struct {
		name     string
		app      string
		ref      tool.VersionRef
		fb       Fallback
		wantID   string
		fellBack bool
		first    bool
		kind     string
		next     string
		sentinel bool
	}{
		{name: "editable", app: "ios", wantID: "edit"},
		{name: "editable first-version check", app: "ios", fb: Fallback{FirstVersion: true}, wantID: "edit"},
		{name: "never live", app: "fresh", fb: Fallback{FirstVersion: true}, wantID: "new", first: true},
		{name: "first-version check skipped", app: "fresh", wantID: "new"},
		// metadata: live fallback only when asked.
		{name: "no fallback", app: "review", kind: "notfound", next: "release_review_status"},
		{name: "live fallback", app: "review", fb: Fallback{Live: true, FirstVersion: true}, wantID: "live", fellBack: true},
		{name: "live fallback explicit editable", app: "review", ref: tool.VersionEditable, fb: Fallback{Live: true}, wantID: "live", fellBack: true},
		{name: "no fallback for exact ref", app: "review", ref: "9.9", fb: Fallback{Live: true}, kind: "notfound", next: "release_prepare"},
		// screenshots: editable only.
		{name: "editable only", app: "review", fb: Fallback{NoEditable: errNoEdit}, next: "release_prepare", sentinel: true},
		{name: "editable only found", app: "ios", fb: Fallback{NoEditable: errNoEdit}, wantID: "edit"},
		{name: "next not created", app: "ios", ref: "next", fb: Fallback{Live: true}, kind: "notfound", next: "release_prepare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tooltest.NewFakeASC(t)
			serveVersions(t, f, map[string][]res{
				"ios": {
					ver("live", "IOS", "2.0", "READY_FOR_DISTRIBUTION", "2026-01-01T00:00:00-08:00"),
					ver("edit", "IOS", "2.1", "PREPARE_FOR_SUBMISSION", "2026-02-01T00:00:00-08:00"),
				},
				"review": {
					ver("live", "IOS", "1.0", "READY_FOR_DISTRIBUTION", "2026-01-01T00:00:00-08:00"),
					ver("wfr", "IOS", "1.1", "WAITING_FOR_REVIEW", "2026-02-01T00:00:00-08:00"),
				},
				"fresh": {ver("new", "IOS", "1.0", "PREPARE_FOR_SUBMISSION", "2026-02-01T00:00:00-08:00")},
			})
			env := &tool.Env{Resolve: idApps{New(f, Options{Clock: tooltest.NewFakeClock()})}}
			got, err := Target(context.Background(), env, tool.AppRef(tt.app), "", tt.ref, tt.fb)
			if tt.sentinel {
				if !errors.Is(err, errNoEdit) {
					t.Fatalf("err = %v, want the NoEditable sentinel", err)
				}
			} else {
				wantErr(t, err, tt.kind)
			}
			if next := firstNext(err); next != tt.next {
				t.Fatalf("next = %q, want %q", next, tt.next)
			}
			if err != nil {
				return
			}
			if got.Version.ID != tt.wantID || got.FellBack != tt.fellBack || got.FirstVersion != tt.first || got.App.ID != tt.app {
				t.Fatalf("got %+v, want id=%s fellBack=%v first=%v", got, tt.wantID, tt.fellBack, tt.first)
			}
		})
	}
}

// firstNext returns the first follow-up tool attached to err: a WithNext
// wrapper wins over the resolver error's own suggestions.
func firstNext(err error) string {
	var w interface{ NextSteps() []tool.NextStep }
	if errors.As(err, &w) && len(w.NextSteps()) > 0 {
		return w.NextSteps()[0].Tool
	}
	var nf *tool.ErrNotFound
	if errors.As(err, &nf) && len(nf.Next) > 0 {
		return nf.Next[0].Tool
	}
	return ""
}
