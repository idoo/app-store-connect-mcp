package tooltest

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// DevSiteCall is one request a [FakeDevSite] saw. Kind is "api", "page" or
// "submit".
type DevSiteCall struct {
	Kind    string
	Method  string
	Path    string
	Query   string // encoded
	TeamID  string
	JSON    any
	Form    string // encoded legacy form
	Referer string
	// RefererQuery is a submit's encoded Referer page query.
	RefererQuery string
	ContentType  string
	Body         string
	Headers      map[string]string
}

// DevSiteHandler answers one routed request.
type DevSiteHandler func(DevSiteCall) (tool.DevSiteAnswer, error)

// FakeDevSite is an in-memory [tool.DevSite]. API routes match
// "METHOD path", pages "PAGE path", form posts "SUBMIT path", all exactly;
// an unrouted request fails the test. Like the real client it refuses a
// write (an API call [tool.DevSiteIsRead] does not call a read, and every
// Submit) without a write context.
type FakeDevSite struct {
	t testing.TB

	mu     sync.Mutex
	routes map[string]DevSiteHandler
	calls  []DevSiteCall
}

// NewFakeDevSite returns an empty FakeDevSite.
func NewFakeDevSite(t testing.TB) *FakeDevSite {
	return &FakeDevSite{t: t, routes: map[string]DevSiteHandler{}}
}

func (f *FakeDevSite) route(key string, fn DevSiteHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[key] = fn
}

// HandleAPI routes a services-account call to fn.
func (f *FakeDevSite) HandleAPI(method, path string, fn DevSiteHandler) {
	f.route(method+" "+path, fn)
}

// HandleAPIJSON routes a services-account call to a fixed 200 JSON body.
func (f *FakeDevSite) HandleAPIJSON(method, path, body string) {
	f.HandleAPI(method, path, func(DevSiteCall) (tool.DevSiteAnswer, error) {
		return tool.DevSiteAnswer{Status: 200, ContentType: "application/json", Body: []byte(body)}, nil
	})
}

// HandlePage routes a page to a fixed 200 HTML body.
func (f *FakeDevSite) HandlePage(path, html string) {
	f.HandlePageFunc(path, func(DevSiteCall) (tool.DevSiteAnswer, error) {
		return tool.DevSiteAnswer{
			Status: 200, ContentType: "text/html; charset=utf-8",
			URL: "https://developer.apple.com/" + path, Body: []byte(html),
		}, nil
	})
}

// HandlePageFunc routes a page to fn.
func (f *FakeDevSite) HandlePageFunc(path string, fn DevSiteHandler) { f.route("PAGE "+path, fn) }

// HandleSubmit routes a form post to fn.
func (f *FakeDevSite) HandleSubmit(path string, fn DevSiteHandler) { f.route("SUBMIT "+path, fn) }

// Calls returns the requests seen so far.
func (f *FakeDevSite) Calls() []DevSiteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DevSiteCall(nil), f.calls...)
}

func (f *FakeDevSite) serve(ctx context.Context, key string, call DevSiteCall, write bool) (tool.DevSiteAnswer, error) {
	if err := ctx.Err(); err != nil {
		return tool.DevSiteAnswer{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fn := f.routes[key]
	f.mu.Unlock()
	if write && !tool.IsWrite(ctx) {
		return tool.DevSiteAnswer{}, fmt.Errorf("tooltest: devsite %s: %w", key, tool.ErrWritesDisabled)
	}
	if fn == nil {
		f.t.Errorf("tooltest: unexpected devsite request %s", key)
		return tool.DevSiteAnswer{}, &tool.PortalError{Status: 404, Messages: []string{"no fake route"}}
	}
	return fn(call)
}

// API implements [tool.DevSite].
func (f *FakeDevSite) API(ctx context.Context, c tool.DevSiteCall) (tool.DevSiteAnswer, error) {
	call := DevSiteCall{
		Kind: "api", Method: c.Method, Path: c.Path, Query: c.Query.Encode(),
		TeamID: c.TeamID, JSON: c.JSON, Form: c.Form.Encode(),
	}
	return f.serve(ctx, c.Method+" "+c.Path, call, !tool.DevSiteIsRead(c.Method, c.Path))
}

// Page implements [tool.DevSite].
func (f *FakeDevSite) Page(ctx context.Context, path string, query url.Values) (tool.DevSiteAnswer, error) {
	return f.serve(ctx, "PAGE "+path, DevSiteCall{Kind: "page", Method: "GET", Path: path, Query: query.Encode()}, false)
}

// Submit implements [tool.DevSite].
func (f *FakeDevSite) Submit(ctx context.Context, p tool.DevSitePost) (tool.DevSiteAnswer, error) {
	call := DevSiteCall{
		Kind: "submit", Method: "POST", Path: p.Path, Query: p.Query.Encode(), Referer: p.Referer,
		RefererQuery: p.RefererQuery.Encode(),
		ContentType:  p.ContentType, Body: string(p.Body), Headers: p.Headers,
	}
	return f.serve(ctx, "SUBMIT "+p.Path, call, true)
}

var _ tool.DevSite = (*FakeDevSite)(nil)
