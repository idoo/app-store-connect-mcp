package browser

import (
	"context"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// frameCallTimeout bounds each best-effort CDP call of the frame runner.
const frameCallTimeout = 2 * time.Second

// frameWorldName names the isolated worlds the frame script runs in.
const frameWorldName = "app-store-connect-mcp"

// frameRunner evaluates Request.FrameScript in every frame of the page,
// including cross-site iframes that Chrome runs out of process (OOPIF; Apple's
// sign-in form lives in one). Same-process frames are reached through the
// page's own session; each OOPIF is a target of type "iframe" that gets its
// own chromedp context. The script runs in an isolated world per frame
// document: it shares the DOM, not the page's JavaScript globals. Every error
// is ignored: the script is a convenience, never a reason to fail sign-in.
type frameRunner struct {
	script  string
	page    *frameSession
	iframes map[target.ID]*frameSession
}

// frameSession is one CDP session (the page or one OOPIF) and the isolated
// worlds created in its frames, keyed by frame and reused while the frame
// keeps the same document (loader).
type frameSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	worlds map[cdp.FrameID]frameWorld
}

type frameWorld struct {
	loader cdp.LoaderID
	id     runtime.ExecutionContextID
}

// newFrameRunner returns nil when there is no script.
func newFrameRunner(pageCtx context.Context, script string) *frameRunner {
	if script == "" {
		return nil
	}
	return &frameRunner{
		script:  script,
		page:    &frameSession{ctx: pageCtx, cancel: func() {}, worlds: map[cdp.FrameID]frameWorld{}},
		iframes: map[target.ID]*frameSession{},
	}
}

// run evaluates the script once in every frame currently present.
func (r *frameRunner) run() {
	if r == nil {
		return
	}
	r.page.eval(r.script)
	r.syncIframes()
	for _, s := range r.iframes {
		s.eval(r.script)
	}
}

// syncIframes attaches to new OOPIF targets and drops the ones that are gone.
func (r *frameRunner) syncIframes() {
	infos, err := chromedp.Targets(r.page.ctx)
	if err != nil {
		return
	}
	seen := make(map[target.ID]bool, len(infos))
	for _, info := range infos {
		if info == nil || info.Type != "iframe" {
			continue
		}
		seen[info.TargetID] = true
		if _, ok := r.iframes[info.TargetID]; ok {
			continue
		}
		// Detach on cancel: closing the context must not close the iframe.
		ctx, cancel := chromedp.NewContext(r.page.ctx, chromedp.WithTargetID(info.TargetID), chromedp.WithDetachOnCancel())
		// The first Do attaches; it must not carry a short timeout, because
		// the attached session lives as long as the context it ran with.
		if err := chromedp.Do(ctx); err != nil {
			cancel()
			continue
		}
		r.iframes[info.TargetID] = &frameSession{ctx: ctx, cancel: cancel, worlds: map[cdp.FrameID]frameWorld{}}
	}
	for id, s := range r.iframes {
		if !seen[id] {
			s.cancel()
			delete(r.iframes, id)
		}
	}
}

// close detaches from every OOPIF target.
func (r *frameRunner) close() {
	if r == nil {
		return
	}
	for id, s := range r.iframes {
		s.cancel()
		delete(r.iframes, id)
	}
}

// eval runs script in every frame this session hosts.
func (s *frameSession) eval(script string) {
	ctx, cancel := context.WithTimeout(s.ctx, frameCallTimeout)
	defer cancel()
	tree, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil || tree.FrameTree == nil {
		return
	}
	live := map[cdp.FrameID]frameWorld{}
	var walk func(t *page.FrameTree)
	walk = func(t *page.FrameTree) {
		if t == nil {
			return
		}
		if f := t.Frame; f != nil {
			if w, ok := s.evalFrame(ctx, f, script); ok {
				live[f.ID] = w
			}
		}
		for _, c := range t.ChildFrames {
			walk(c)
		}
	}
	walk(tree.FrameTree)
	s.worlds = live
}

// evalFrame evaluates script in f's isolated world, creating the world for a
// new document. ok=false drops the world so the next tick recreates it.
func (s *frameSession) evalFrame(ctx context.Context, f *cdp.Frame, script string) (frameWorld, bool) {
	w, ok := s.worlds[f.ID]
	if !ok || w.loader != f.LoaderID {
		res, err := chromedp.Call(ctx, page.CreateIsolatedWorld, page.CreateIsolatedWorldParams{FrameID: f.ID, WorldName: frameWorldName})
		if err != nil {
			// A frame hosted by another process (OOPIF) fails here; its own
			// session handles it.
			return frameWorld{}, false
		}
		w = frameWorld{loader: f.LoaderID, id: res.ExecutionContextID}
	}
	silent := true
	// The script's value and exceptions are deliberately ignored.
	if _, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{Expression: script, ContextID: w.id, Silent: &silent}); err != nil {
		return frameWorld{}, false
	}
	return w, true
}
