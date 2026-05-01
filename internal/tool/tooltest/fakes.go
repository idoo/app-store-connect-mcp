package tooltest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// PlanCapacity is the number of pending plans kept per session (LRU).
const PlanCapacity = 32

// FakePlanner is an in-memory [tool.Planner] with the resolved semantics: random
// plan ids, plans bound to tool, canonical args and fingerprint, consumed on
// use, at most PlanCapacity pending (oldest evicted), and an empty
// fingerprint refused (fail closed).
type FakePlanner struct {
	mu    sync.Mutex
	order []string
	plans map[string]plan
	// Proposed records every preview proposed, in order.
	Proposed []tool.PlanPreview
}

type plan struct {
	tool, args, fingerprint string
}

// NewFakePlanner returns an empty FakePlanner.
func NewFakePlanner() *FakePlanner { return &FakePlanner{plans: map[string]plan{}} }

// Propose implements tool.Planner.
func (p *FakePlanner) Propose(_ context.Context, toolName string, args any, fingerprint string, preview tool.PlanPreview) (string, error) {
	if fingerprint == "" {
		return "", fmt.Errorf("%w: empty fingerprint", tool.ErrInternal)
	}
	canon, err := tool.CanonicalArgs(args)
	if err != nil {
		return "", err
	}
	id := "plan_" + rand.Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans[id] = plan{tool: toolName, args: string(canon), fingerprint: fingerprint}
	p.order = append(p.order, id)
	for len(p.order) > PlanCapacity {
		delete(p.plans, p.order[0])
		p.order = p.order[1:]
	}
	p.Proposed = append(p.Proposed, preview)
	return id, nil
}

// Verify implements tool.Planner.
func (p *FakePlanner) Verify(_ context.Context, planID, toolName string, args any, fingerprint string) error {
	canon, err := tool.CanonicalArgs(args)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pl, ok := p.plans[planID]
	switch {
	case !ok:
		return fmt.Errorf("%w: unknown plan %q", tool.ErrStalePlan, planID)
	case pl.tool != toolName:
		return fmt.Errorf("%w: plan %q belongs to %s", tool.ErrStalePlan, planID, pl.tool)
	case pl.args != string(canon):
		return fmt.Errorf("%w: arguments differ from the planned call", tool.ErrStalePlan)
	case pl.fingerprint != fingerprint:
		return fmt.Errorf("%w: remote state changed since the plan was made", tool.ErrStalePlan)
	}
	delete(p.plans, planID)
	p.order = slices.DeleteFunc(p.order, func(s string) bool { return s == planID })
	return nil
}

// FakeStore is an in-memory [tool.StateStore].
type FakeStore struct {
	mu      sync.Mutex
	cursors map[string]string
	records map[string][]byte
}

// NewFakeStore returns an empty FakeStore.
func NewFakeStore() *FakeStore {
	return &FakeStore{cursors: map[string]string{}, records: map[string][]byte{}}
}

// Cursor implements tool.StateStore.
func (s *FakeStore) Cursor(profile, app, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[profile+"/"+app+"/"+name], nil
}

// SetCursor implements tool.StateStore.
func (s *FakeStore) SetCursor(profile, app, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[profile+"/"+app+"/"+name] = value
	return nil
}

// Load implements tool.StateStore.
func (s *FakeStore) Load(kind, key string, v any) (bool, error) {
	s.mu.Lock()
	b, ok := s.records[kind+"/"+key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, fmt.Errorf("load %s/%s: %w", kind, key, err)
	}
	return true, nil
}

// Save implements tool.StateStore.
func (s *FakeStore) Save(kind, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("save %s/%s: %w", kind, key, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[kind+"/"+key] = b
	return nil
}

// Delete implements tool.StateStore.
func (s *FakeStore) Delete(kind, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, kind+"/"+key)
	return nil
}

// FakeFiles is a [tool.Files] confined to a temporary directory (Root).
type FakeFiles struct {
	Root string
}

// NewFakeFiles returns FakeFiles rooted at t.TempDir() (symlinks resolved).
func NewFakeFiles(t testing.TB) *FakeFiles {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	return &FakeFiles{Root: root}
}

// Resolve implements tool.Files: relative paths are joined to Root, and the
// resolved path (symlinks resolved on the longest existing prefix) must stay
// inside Root.
func (f *FakeFiles) Resolve(path string) (string, error) {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(f.Root, p)
	}
	p = filepath.Clean(p)
	existing, rest := p, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	resolved = filepath.Join(resolved, rest)
	rel, err := filepath.Rel(f.Root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s is outside the allowed root", path)
	}
	return resolved, nil
}

// ReadFile implements tool.Files.
func (f *FakeFiles) ReadFile(path string) ([]byte, error) {
	p, err := f.Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p) //nolint:gosec // confined by Resolve
}

// WriteFile implements tool.Files.
func (f *FakeFiles) WriteFile(path string, data []byte, perm fs.FileMode) error {
	p, err := f.Resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.WriteFile(p, data, perm)
}

// Open implements tool.Files.
func (f *FakeFiles) Open(path string) (*os.File, error) {
	p, err := f.Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Open(p) //nolint:gosec // confined by Resolve
}

// Stat implements tool.Files.
func (f *FakeFiles) Stat(path string) (fs.FileInfo, error) {
	p, err := f.Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Stat(p)
}

// WalkDir implements tool.Files.
func (f *FakeFiles) WalkDir(root string, fn fs.WalkDirFunc) error {
	p, err := f.Resolve(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(p, fn)
}

// FakeFetcher is a [tool.Fetcher] serving canned responses by exact URL. It
// enforces https and the forbidden-header rule and fails the test on
// unknown URLs.
type FakeFetcher struct {
	t     testing.TB
	mu    sync.Mutex
	resps map[string]tool.FetchResponse
	// Calls records "METHOD URL" for every fetch.
	Calls []string
}

// NewFakeFetcher returns an empty FakeFetcher.
func NewFakeFetcher(t testing.TB) *FakeFetcher {
	return &FakeFetcher{t: t, resps: map[string]tool.FetchResponse{}}
}

// Handle registers a response for method+URL.
func (f *FakeFetcher) Handle(method, rawURL string, status int, body []byte) *FakeFetcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resps[method+" "+rawURL] = tool.FetchResponse{Status: status, Header: http.Header{}, Body: body}
	return f
}

// Fetch implements tool.Fetcher.
func (f *FakeFetcher) Fetch(_ context.Context, method, rawURL string, header http.Header, body io.Reader, maxBytes int64) (*tool.FetchResponse, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, fmt.Errorf("url policy: %q refused", rawURL)
	}
	for k := range header {
		switch k = strings.ToLower(k); {
		case k == "authorization", k == "cookie", k == "host", strings.HasPrefix(k, "proxy-"):
			return nil, fmt.Errorf("url policy: header %q refused", k)
		}
	}
	if body != nil {
		if _, err := io.Copy(io.Discard, body); err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, method+" "+rawURL)
	r, ok := f.resps[method+" "+rawURL]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("FakeFetcher: unexpected %s %s", method, rawURL)
		return nil, fmt.Errorf("FakeFetcher: no response for %s %s", method, rawURL)
	}
	if maxBytes > 0 && int64(len(r.Body)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	r.Body = bytes.Clone(r.Body)
	return &r, nil
}

// FakeToolsets is a [tool.Toolsets] over the global registry.
type FakeToolsets struct {
	mu      sync.Mutex
	enabled map[tool.Toolset]bool
}

// NewFakeToolsets returns toolsets with the defaults enabled.
func NewFakeToolsets() *FakeToolsets {
	e := map[tool.Toolset]bool{}
	for _, ts := range tool.DefaultToolsets {
		e[ts] = true
	}
	return &FakeToolsets{enabled: e}
}

// List implements tool.Toolsets.
func (f *FakeToolsets) List() []tool.ToolsetInfo {
	counts := map[tool.Toolset]int{}
	for _, t := range tool.All() {
		counts[t.Meta().Toolset]++
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tool.ToolsetInfo, 0, len(tool.AllToolsets))
	for _, ts := range tool.AllToolsets {
		out = append(out, tool.ToolsetInfo{Name: ts, Enabled: f.enabled[ts], Default: ts.IsDefault(), Tools: counts[ts]})
	}
	return out
}

// Enable implements tool.Toolsets.
func (f *FakeToolsets) Enable(_ context.Context, names ...tool.Toolset) error {
	return f.set(true, names)
}

// Disable implements tool.Toolsets.
func (f *FakeToolsets) Disable(_ context.Context, names ...tool.Toolset) error {
	return f.set(false, names)
}

func (f *FakeToolsets) set(on bool, names []tool.Toolset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range names {
		if !n.Valid() {
			return fmt.Errorf("%w: unknown toolset %q", tool.ErrInvalidArgs, n)
		}
		if n == tool.Core && !on {
			return fmt.Errorf("%w: core cannot be disabled", tool.ErrInvalidArgs)
		}
		f.enabled[n] = on
	}
	return nil
}

// FakeClock is a [tool.Clock] whose Sleep advances time instantly.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
	// Slept accumulates every Sleep duration.
	Slept time.Duration
}

// NewFakeClock starts at 2026-01-02T03:04:05Z.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

// Now implements tool.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep implements tool.Clock.
func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.Slept += d
	return nil
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}
