package tooltest

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/ascrules"
	"github.com/idoo/hamn/internal/tool"
)

// Fixture is one canned response.
type Fixture struct {
	Status int
	Header http.Header
	Body   []byte
	// Err, when set, is returned instead of a response (e.g. a timeout).
	Err error
}

type route struct {
	method   string
	segments []string
	query    url.Values
	fn       func(tool.Request) Fixture
	queue    []Fixture
	served   int
}

// FakeASC is an in-memory [tool.ASC]. Routes match method plus a path
// pattern whose "{name}" segments match any single segment; an optional
// "?k=v" suffix requires those query values. Several responses registered
// for the same route are served in order, the last one repeating.
// Unmatched requests fail the test. Like the real client, it refuses any
// non-GET request lacking Request.Write and the context write capability,
// and it retries a 404 GET flagged with [ascrules.WithEventualConsistency].
// When Spec is set (as [New] does), every request must validate against it
// (method, path, query and body) or the test fails.
type FakeASC struct {
	t      testing.TB
	mu     sync.Mutex
	routes []*route
	reqs   []tool.Request
	// Limit is returned by RateLimit.
	Limit tool.RateLimit
	// Skew is returned by ClockSkew.
	Skew time.Duration
	// Spec, when set, validates every request.
	Spec tool.Spec
	// SkipSpecCheck disables the Spec validation for tests that send
	// deliberately invalid or unlisted requests.
	SkipSpecCheck bool
}

// NewFakeASC returns an empty FakeASC that does not validate requests.
func NewFakeASC(t testing.TB) *FakeASC { return &FakeASC{t: t} }

// Handle registers an inline response body (JSON or raw).
func (f *FakeASC) Handle(method, pattern string, status int, body string) *FakeASC {
	return f.add(method, pattern, Fixture{Status: status, Body: []byte(body)})
}

// HandleFile registers a 200 response whose body is read from path (relative
// to the test's package directory).
func (f *FakeASC) HandleFile(method, pattern, path string) *FakeASC {
	f.t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path chosen by the test author
	if err != nil {
		f.t.Fatalf("fixture %s: %v", path, err)
	}
	return f.add(method, pattern, Fixture{Status: http.StatusOK, Body: b})
}

// HandleFixture registers a full fixture (status, headers, raw bytes, error).
func (f *FakeASC) HandleFixture(method, pattern string, fx Fixture) *FakeASC {
	return f.add(method, pattern, fx)
}

// HandleFunc registers a dynamic responder.
func (f *FakeASC) HandleFunc(method, pattern string, fn func(tool.Request) Fixture) *FakeASC {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.route(method, pattern)
	r.fn = fn
	return f
}

func (f *FakeASC) add(method, pattern string, fx Fixture) *FakeASC {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.route(method, pattern)
	r.queue = append(r.queue, fx)
	return f
}

// route finds or creates the route for method+pattern; f.mu must be held.
func (f *FakeASC) route(method, pattern string) *route {
	p, q, _ := strings.Cut(pattern, "?")
	query, err := url.ParseQuery(q)
	if err != nil {
		f.t.Fatalf("route %s: %v", pattern, err)
	}
	segs := splitPath(p)
	for _, r := range f.routes {
		if r.method == method && slices.Equal(r.segments, segs) && r.query.Encode() == query.Encode() {
			return r
		}
	}
	r := &route{method: method, segments: segs, query: query}
	f.routes = append(f.routes, r)
	return r
}

func splitPath(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

func (r *route) matches(method, path string, q url.Values) bool {
	if r.method != method {
		return false
	}
	segs := splitPath(path)
	if len(segs) != len(r.segments) {
		return false
	}
	for i, s := range r.segments {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			if segs[i] == "" {
				return false
			}
			continue
		}
		if s != segs[i] {
			return false
		}
	}
	for k, vs := range r.query {
		if !slices.Equal(q[k], vs) {
			return false
		}
	}
	return true
}

// normalize splits an inline query string out of req.Path.
func normalize(req tool.Request) tool.Request {
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	if p, q, ok := strings.Cut(req.Path, "?"); ok {
		req.Path = p
		if req.Query == nil {
			req.Query, _ = url.ParseQuery(q)
		}
	}
	return req
}

func (f *FakeASC) serve(ctx context.Context, req tool.Request) (Fixture, error) {
	for i := 0; ; i++ {
		fx, err := f.serveOnce(ctx, req)
		if i >= ascrules.ConsistencyRetries || fx.Status != http.StatusNotFound ||
			normalize(req).Method != http.MethodGet || !ascrules.IsEventualConsistency(ctx) {
			return fx, err
		}
	}
}

// checkSpec fails the test when req does not validate against f.Spec.
func (f *FakeASC) checkSpec(req tool.Request) {
	f.mu.Lock()
	s, skip := f.Spec, f.SkipSpecCheck
	f.mu.Unlock()
	if s == nil || skip {
		return
	}
	if err := s.Validate(req); err != nil {
		f.t.Errorf("FakeASC: %s %s?%s violates the pinned spec: %v (set Kit.ASC.SkipSpecCheck for deliberate invalid requests)",
			req.Method, req.Path, req.Query.Encode(), err)
	}
}

func (f *FakeASC) serveOnce(ctx context.Context, req tool.Request) (Fixture, error) {
	req = normalize(req)
	f.checkSpec(req)
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	var hit *route
	// Most specific route first: those with query constraints win.
	for _, r := range f.routes {
		if r.matches(req.Method, req.Path, req.Query) && (hit == nil || len(r.query) > len(hit.query)) {
			hit = r
		}
	}
	var fx Fixture
	if hit != nil {
		switch {
		case hit.fn != nil:
			fn := hit.fn
			f.mu.Unlock()
			fx = fn(req)
			f.mu.Lock()
		case len(hit.queue) > 0:
			fx = hit.queue[min(hit.served, len(hit.queue)-1)]
		}
		hit.served++
	}
	f.mu.Unlock()

	if req.Method != http.MethodGet && (!req.Write || !tool.IsWrite(ctx)) {
		return Fixture{}, fmt.Errorf("%s %s: %w", req.Method, req.Path, tool.ErrWritesDisabled)
	}
	if hit == nil {
		f.t.Errorf("FakeASC: unexpected request %s %s?%s", req.Method, req.Path, req.Query.Encode())
		return Fixture{}, fmt.Errorf("FakeASC: no route for %s %s", req.Method, req.Path)
	}
	if fx.Err != nil {
		return Fixture{}, fx.Err
	}
	if fx.Status == 0 {
		fx.Status = http.StatusOK
	}
	if fx.Status >= http.StatusMultipleChoices {
		return fx, apiError(fx)
	}
	return fx, nil
}

func apiError(fx Fixture) error {
	if doc, err := tool.ParseDocument(fx.Body); err == nil {
		if e := doc.Err(fx.Status); e != nil {
			return e
		}
	}
	return &tool.APIError{Status: fx.Status, Title: http.StatusText(fx.Status)}
}

// Do implements tool.ASC.
func (f *FakeASC) Do(ctx context.Context, req tool.Request) (*tool.Response, error) {
	fx, err := f.serve(ctx, req)
	if err != nil {
		return nil, err
	}
	doc, err := tool.ParseDocument(fx.Body)
	if err != nil {
		return nil, err
	}
	return &tool.Response{
		Status: fx.Status, Header: fx.Header, Data: doc.Data, Collection: doc.Collection,
		Included: doc.Included, Meta: doc.Meta, Links: doc.Links, Body: fx.Body,
	}, nil
}

// Get implements tool.ASC.
func (f *FakeASC) Get(ctx context.Context, path string, query url.Values) (*tool.Response, error) {
	return f.Do(ctx, tool.Request{Method: http.MethodGet, Path: path, Query: query})
}

// Download implements tool.ASC; register raw bytes with HandleFixture.
func (f *FakeASC) Download(ctx context.Context, req tool.Request) ([]byte, http.Header, error) {
	req.Method = http.MethodGet
	fx, err := f.serve(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return fx.Body, fx.Header, nil
}

// List implements tool.ASC with the real client's semantics: follows
// links.next, dedups included by type+id, caps at MaxItems (default 200) and
// returns next_cursor as base64 of the next URL.
func (f *FakeASC) List(ctx context.Context, path string, opts tool.ListOptions) (*tool.ListResult, error) {
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = 200
	}
	q := url.Values{}
	for k, vs := range opts.Query {
		q[k] = slices.Clone(vs)
	}
	for typ, fields := range opts.Fields {
		q.Set("fields["+typ+"]", strings.Join(fields, ","))
	}
	if len(opts.Include) > 0 {
		q.Set("include", strings.Join(opts.Include, ","))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		next, err := decodeCursor(opts.Cursor)
		if err != nil {
			return nil, err
		}
		path, q = next.Path, next.Query()
	}
	out := &tool.ListResult{}
	seen := map[string]bool{}
	visited := map[string]bool{}
	for {
		resp, err := f.Get(ctx, path, q)
		if err != nil {
			return nil, err
		}
		if total, ok := pagingTotal(resp.Meta); ok {
			out.Total = total
		}
		// Items beyond MaxItems on the current page are dropped; the cursor
		// resumes at the following page (page size should divide MaxItems).
		for _, r := range resp.Data {
			if len(out.Data) == maxItems {
				break
			}
			out.Data = append(out.Data, r)
		}
		for _, r := range resp.Included {
			key := r.Type + "/" + r.ID
			if !seen[key] {
				seen[key] = true
				out.Included = append(out.Included, r)
			}
		}
		next := resp.Links["next"]
		if next == "" {
			break
		}
		if len(out.Data) >= maxItems {
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(next))
			break
		}
		if visited[next] {
			return nil, fmt.Errorf("pagination loop at %s", next)
		}
		visited[next] = true
		u, err := url.Parse(next)
		if err != nil {
			return nil, fmt.Errorf("next link: %w", err)
		}
		path, q = u.Path, u.Query()
	}
	out.Returned = len(out.Data)
	return out, nil
}

func decodeCursor(c string) (*url.URL, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, fmt.Errorf("%w: bad cursor", tool.ErrInvalidArgs)
	}
	u, err := url.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%w: bad cursor", tool.ErrInvalidArgs)
	}
	return u, nil
}

func pagingTotal(meta map[string]any) (int, bool) {
	p, _ := meta["paging"].(map[string]any)
	t, ok := p["total"].(float64)
	return int(t), ok
}

// RateLimit implements tool.ASC.
func (f *FakeASC) RateLimit() tool.RateLimit { return f.Limit }

// ClockSkew implements tool.ASC.
func (f *FakeASC) ClockSkew() (time.Duration, bool) { return f.Skew, true }

// Requests returns every request received, in order.
func (f *FakeASC) Requests() []tool.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// RequestsTo returns the requests matching method and pattern.
func (f *FakeASC) RequestsTo(method, pattern string) []tool.Request {
	f.mu.Lock()
	r := &route{method: method}
	p, q, _ := strings.Cut(pattern, "?")
	r.segments = splitPath(p)
	r.query, _ = url.ParseQuery(q)
	reqs := slices.Clone(f.reqs)
	f.mu.Unlock()
	var out []tool.Request
	for _, req := range reqs {
		if r.matches(req.Method, req.Path, req.Query) {
			out = append(out, req)
		}
	}
	return out
}

// Mutations returns every non-GET request received.
func (f *FakeASC) Mutations() []tool.Request {
	var out []tool.Request
	for _, r := range f.Requests() {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}
