package tooltest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// PortalCall records one [tool.Portal] request seen by a [FakePortal].
type PortalCall struct {
	Method string
	Path   string
	// Body is the JSON-encoded request body ("" when none).
	Body string
}

// PortalHandler answers one request: a JSON response body (decoded into
// the caller's out) or an error such as *tool.PortalError.
type PortalHandler func(call PortalCall) (string, error)

// FakePortal is an in-memory [tool.Portal]. Routes match "METHOD path"
// exactly, the query included; an unrouted request fails the test.
type FakePortal struct {
	t testing.TB

	mu     sync.Mutex
	routes map[string]PortalHandler
	calls  []PortalCall
}

// NewFakePortal returns an empty FakePortal.
func NewFakePortal(t testing.TB) *FakePortal {
	return &FakePortal{t: t, routes: map[string]PortalHandler{}}
}

// Handle routes method+path to fn.
func (f *FakePortal) Handle(method, path string, fn PortalHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = fn
}

// HandleJSON routes method+path to a fixed JSON body.
func (f *FakePortal) HandleJSON(method, path, body string) {
	f.Handle(method, path, func(PortalCall) (string, error) { return body, nil })
}

// HandleFile routes method+path to the JSON in file.
func (f *FakePortal) HandleFile(method, path, file string) {
	f.t.Helper()
	b, err := os.ReadFile(file) //nolint:gosec // test fixture path chosen by the test author
	if err != nil {
		f.t.Fatalf("tooltest: portal fixture: %v", err)
	}
	f.HandleJSON(method, path, string(b))
}

// HandleError routes method+path to a *tool.PortalError.
func (f *FakePortal) HandleError(method, path string, status int, messages ...string) {
	f.Handle(method, path, func(PortalCall) (string, error) {
		return "", &tool.PortalError{Status: status, Messages: messages}
	})
}

// Calls returns the requests seen so far.
func (f *FakePortal) Calls() []PortalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PortalCall(nil), f.calls...)
}

// JSON implements [tool.Portal].
func (f *FakePortal) JSON(ctx context.Context, method, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	call := PortalCall{Method: method, Path: path}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tooltest: portal body: %w", err)
		}
		call.Body = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fn := f.routes[method+" "+path]
	f.mu.Unlock()
	if fn == nil {
		f.t.Errorf("tooltest: unexpected portal request %s %s", method, path)
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
