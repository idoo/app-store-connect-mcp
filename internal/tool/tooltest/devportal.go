package tooltest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// FakeDevPortal is an in-memory [tool.DevPortal]. Routes match "METHOD path"
// exactly (reads through [tool.DevPortalGet] use method "GET"); an unrouted
// request fails the test. Calls are recorded like [FakePortal]'s.
type FakeDevPortal struct {
	t testing.TB

	mu     sync.Mutex
	routes map[string]PortalHandler
	calls  []PortalCall
}

// NewFakeDevPortal returns an empty FakeDevPortal.
func NewFakeDevPortal(t testing.TB) *FakeDevPortal {
	return &FakeDevPortal{t: t, routes: map[string]PortalHandler{}}
}

// Handle routes method+path to fn.
func (f *FakeDevPortal) Handle(method, path string, fn PortalHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = fn
}

// HandleJSON routes method+path to a fixed JSON body.
func (f *FakeDevPortal) HandleJSON(method, path, body string) {
	f.Handle(method, path, func(PortalCall) (string, error) { return body, nil })
}

// HandleError routes method+path to a *tool.PortalError.
func (f *FakeDevPortal) HandleError(method, path string, status int, messages ...string) {
	f.Handle(method, path, func(PortalCall) (string, error) {
		return "", &tool.PortalError{Status: status, Messages: messages}
	})
}

// Calls returns the requests seen so far.
func (f *FakeDevPortal) Calls() []PortalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PortalCall(nil), f.calls...)
}

// Do implements [tool.DevPortal].
func (f *FakeDevPortal) Do(ctx context.Context, method, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	call := PortalCall{Method: method, Path: path}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tooltest: devportal body: %w", err)
		}
		call.Body = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fn := f.routes[method+" "+path]
	f.mu.Unlock()
	if fn == nil {
		f.t.Errorf("tooltest: unexpected devportal request %s %s", method, path)
		return &tool.PortalError{Status: 404, Messages: []string{"no fake route"}}
	}
	resp, err := fn(call)
	if err != nil {
		return err
	}
	if out == nil || strings.TrimSpace(resp) == "" {
		return nil
	}
	return json.Unmarshal([]byte(resp), out)
}
