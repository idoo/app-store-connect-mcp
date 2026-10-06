package asctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/spec"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// AppleBaseURL is rewritten to the fake server's URL in every served body,
// so recorded links.next and related links stay inside the fake.
const AppleBaseURL = "https://api.appstoreconnect.apple.com"

// maxRequestBody bounds a request body read by the fake.
const maxRequestBody = 16 << 20

// Request is one request received by the fake.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	// Op is the spec operation id the request matched ("" if none).
	Op string
}

// Response is a canned reply.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// route is one registered method+pattern with its queued responses.
type route struct {
	method string
	segs   []string
	query  url.Values
	queue  []Response
	served int
}

// Server is an httptest server faking App Store Connect. Every request is
// checked against the pinned spec index: an unknown operation, unknown query
// parameter, invalid enum value or invalid JSON:API body fails the test
// (t.Errorf) and is answered with 400, which makes every tool test a
// contract test. Requests without a bearer token fail the test too.
//
// Routes match a method and a path pattern whose "{name}" segments match any
// one segment; an optional "?k=v" suffix requires those query values. Routes
// are tried in registration order. Registering the same route again queues
// another response; the last one repeats. Unmatched requests fail the test
// and get 404.
type Server struct {
	*httptest.Server

	t    testing.TB
	spec *spec.Index

	mu     sync.Mutex
	routes []*route
	reqs   []Request
}

// New starts a Server that closes when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	idx, err := spec.Load()
	if err != nil {
		t.Fatalf("asctest: load spec: %v", err)
	}
	s := &Server{t: t, spec: idx}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Handle queues a response with an inline body (JSON or raw) for
// method+pattern. The pattern itself must be a spec operation.
func (s *Server) Handle(method, pattern string, status int, body string) *Server {
	s.t.Helper()
	return s.HandleResponse(method, pattern, Response{Status: status, Body: []byte(body)})
}

// HandleResponse queues a full response for method+pattern.
func (s *Server) HandleResponse(method, pattern string, resp Response) *Server {
	s.t.Helper()
	method = strings.ToUpper(method)
	p, q, _ := strings.Cut(pattern, "?")
	query, err := url.ParseQuery(q)
	if err != nil {
		s.t.Fatalf("asctest: route %s %s: %v", method, pattern, err)
	}
	if _, ok := s.spec.Match(method, p); !ok {
		s.t.Fatalf("asctest: route %s %s is not an operation of spec %s", method, p, s.spec.Version())
	}
	segs := splitPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.routes {
		if r.method == method && slices.Equal(r.segs, segs) && queryEqual(r.query, query) {
			r.queue = append(r.queue, resp)
			return s
		}
	}
	s.routes = append(s.routes, &route{method: method, segs: segs, query: query, queue: []Response{resp}})
	return s
}

// Requests returns a copy of every request received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// serve validates, records and answers one request.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Sprintf("asctest: read body of %s %s: %v", r.Method, r.URL.Path, err))
		return
	}
	req := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
	if op, ok := s.spec.Match(r.Method, r.URL.Path); ok {
		req.Op = op.ID
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()

	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.fail(w, http.StatusUnauthorized, fmt.Sprintf("asctest: %s %s sent without a bearer token", r.Method, r.URL.Path))
		return
	}
	if msg := s.check(r, body); msg != "" {
		s.fail(w, http.StatusBadRequest, msg)
		return
	}
	resp, ok := s.next(r.Method, r.URL.Path, req.Query)
	if !ok {
		s.fail(w, http.StatusNotFound, fmt.Sprintf("asctest: no handler for %s %s (op %s)", r.Method, r.URL.RequestURI(), req.Op))
		return
	}
	for k, vs := range resp.Header {
		w.Header()[k] = append([]string(nil), vs...)
	}
	out := bytes.ReplaceAll(resp.Body, []byte(AppleBaseURL), []byte(s.URL))
	if len(out) > 0 && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// check validates the request against the spec and returns a failure
// message, or "" when it conforms.
func (s *Server) check(r *http.Request, body []byte) string {
	treq := tool.Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), ContentType: r.Header.Get("Content-Type")}
	if len(body) > 0 {
		treq.RawBody = body
	}
	err := s.spec.Validate(treq)
	if err == nil {
		return ""
	}
	if ve, ok := err.(*tool.ValidationError); ok {
		return fmt.Sprintf("asctest: %s %s violates spec %s: %s", r.Method, r.URL.RequestURI(), s.spec.Version(), strings.Join(ve.Problems, "; "))
	}
	return fmt.Sprintf("asctest: %s %s: %v", r.Method, r.URL.RequestURI(), err)
}

// next pops the response of the first matching route.
func (s *Server) next(method, path string, q url.Values) (Response, bool) {
	segs := splitPath(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.routes {
		if !rt.matches(method, segs, q) {
			continue
		}
		i := min(rt.served, len(rt.queue)-1)
		rt.served++
		return rt.queue[i], true
	}
	return Response{}, false
}

// fail reports msg to the test and answers with a JSON:API error document.
func (s *Server) fail(w http.ResponseWriter, status int, msg string) {
	s.t.Errorf("%s", msg)
	doc := map[string]any{"errors": []map[string]string{{
		"status": fmt.Sprint(status),
		"code":   "ASCTEST_REJECTED",
		"title":  http.StatusText(status),
		"detail": msg,
	}}}
	b, _ := json.Marshal(doc)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// matches reports whether the route serves method+segs with query q.
func (rt *route) matches(method string, segs []string, q url.Values) bool {
	if rt.method != method || len(rt.segs) != len(segs) {
		return false
	}
	for i, p := range rt.segs {
		if p != segs[i] && (!strings.HasPrefix(p, "{") || !strings.HasSuffix(p, "}")) {
			return false
		}
	}
	for k, want := range rt.query {
		got := q[k]
		for _, v := range want {
			if !slices.Contains(got, v) {
				return false
			}
		}
	}
	return true
}

func splitPath(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

func queryEqual(a, b url.Values) bool { return a.Encode() == b.Encode() }

// readFile reads a test-chosen file.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // fixture path chosen by the test author
}
