package asctest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recTB records Errorf calls instead of failing, so tests can assert that
// the fake rejects bad requests.
type recTB struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recTB) messages() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.errs, "\n")
}

const appsPage = `{"data":[{"type":"apps","id":"1","attributes":{"name":"A"},"links":{"self":"https://api.appstoreconnect.apple.com/v1/apps/1"}}],"links":{"self":"https://api.appstoreconnect.apple.com/v1/apps"}}`

func rawDo(t *testing.T, s *Server, method, path, body string, auth bool) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+TestToken)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestServerAcceptsSpecRequestThroughClient(t *testing.T) {
	s := New(t).Handle("GET", "/v1/apps", 200, appsPage)
	c := s.NewClient(t)
	resp, err := c.Get(context.Background(), "/v1/apps", url.Values{"limit": {"1"}, "fields[apps]": {"name,bundleId"}})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r, ok := resp.First(); !ok || r.ID != "1" || r.Type != "apps" {
		t.Fatalf("first = %+v", resp.Data)
	}
	reqs := s.Requests()
	if len(reqs) != 1 || reqs[0].Op != "apps_getCollection" || reqs[0].Query.Get("limit") != "1" {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer "+TestToken {
		t.Fatalf("authorization = %q", got)
	}
}

func TestServerRejects(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		noAuth                   bool
		status                   int
		want                     string
	}{
		{"unknown path", "GET", "/v1/nope", "", false, 400, "no operation matches GET /v1/nope"},
		{"wrong method", "DELETE", "/v1/apps", "", false, 400, "method DELETE is not defined"},
		{"unknown query key", "GET", "/v1/apps?filter[bogus]=1", "", false, 400, `unknown query parameter "filter[bogus]"`},
		{"bad enum", "GET", "/v1/apps?fields[apps]=nonsense", "", false, 400, `fields[apps]: "nonsense" is not one of`},
		{"bad integer", "GET", "/v1/apps?limit=many", "", false, 400, "is not an integer"},
		{"wrong body type", "POST", "/v1/appStoreVersions", `{"data":{"type":"apps","attributes":{}}}`, false, 400, "data.type"},
		{"missing body", "POST", "/v1/appStoreVersions", "", false, 400, "requires a request body"},
		{"no bearer", "GET", "/v1/apps", "", true, 401, "without a bearer token"},
		{"unregistered route", "GET", "/v1/apps/9", "", false, 404, "no handler for GET /v1/apps/9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recTB{TB: t}
			s := New(rec).Handle("GET", "/v1/apps", 200, appsPage)
			status, body := rawDo(t, s, tt.method, tt.path, tt.body, !tt.noAuth)
			if status != tt.status {
				t.Errorf("status = %d, want %d (%s)", status, tt.status, body)
			}
			if msgs := rec.messages(); !strings.Contains(msgs, tt.want) {
				t.Errorf("test failures = %q, want %q", msgs, tt.want)
			}
			if !strings.Contains(body, "ASCTEST_REJECTED") {
				t.Errorf("body = %s, want JSON:API error", body)
			}
		})
	}
}

func TestServerAcceptsValidBody(t *testing.T) {
	rec := &recTB{TB: t}
	body := `{"data":{"type":"appStoreVersions","attributes":{"platform":"IOS","versionString":"1.0"},"relationships":{"app":{"data":{"type":"apps","id":"1"}}}}}`
	s := New(rec).Handle("POST", "/v1/appStoreVersions", 201, `{"data":{"type":"appStoreVersions","id":"v1"}}`)
	status, resp := rawDo(t, s, "POST", "/v1/appStoreVersions", body, true)
	if status != 201 || rec.messages() != "" {
		t.Fatalf("status=%d body=%s failures=%q", status, resp, rec.messages())
	}
	bad := strings.Replace(body, `"IOS"`, `"PALM_OS"`, 1)
	if status, _ := rawDo(t, s, "POST", "/v1/appStoreVersions", bad, true); status != 400 || !strings.Contains(rec.messages(), "PALM_OS") {
		t.Fatalf("bad enum: status=%d failures=%q", status, rec.messages())
	}
}

func TestServerQueueAndRewrite(t *testing.T) {
	s := New(t).
		Handle("GET", "/v1/apps/{id}", 200, `{"data":{"type":"apps","id":"first"}}`).
		Handle("GET", "/v1/apps/{id}", 200, `{"data":{"type":"apps","id":"second","links":{"self":"https://api.appstoreconnect.apple.com/v1/apps/second"}}}`).
		Handle("GET", "/v1/apps?filter[bundleId]=com.x", 200, `{"data":[]}`)
	var got []string
	for range 3 {
		_, body := rawDo(t, s, "GET", "/v1/apps/7", "", true)
		got = append(got, body)
	}
	if !strings.Contains(got[0], "first") || !strings.Contains(got[1], "second") || !strings.Contains(got[2], "second") {
		t.Fatalf("queue order = %q", got)
	}
	if !strings.Contains(got[1], s.URL+"/v1/apps/second") || strings.Contains(got[1], AppleBaseURL) {
		t.Fatalf("base URL not rewritten: %s", got[1])
	}
	if status, body := rawDo(t, s, "GET", "/v1/apps?filter%5BbundleId%5D=com.x", "", true); status != 200 || body != `{"data":[]}` {
		t.Fatalf("query route: %d %s", status, body)
	}
}

func TestFixtureProblems(t *testing.T) {
	list, _ := lookupOp(t, "apps_getCollection")
	one, _ := lookupOp(t, "apps_getInstance")
	tests := []struct {
		name string
		op   string
		body string
		want string
	}{
		{"collection ok", "list", `{"data":[{"type":"apps","id":"1"}],"included":[{"type":"builds","id":"b"}]}`, ""},
		{"single ok", "one", `{"data":{"type":"apps","id":"1"}}`, ""},
		{"errors ok", "one", `{"errors":[{"status":"404","code":"NOT_FOUND"}]}`, ""},
		{"not object", "one", `[1]`, "not a JSON object"},
		{"no data", "one", `{"meta":{}}`, `missing top-level "data"`},
		{"object for collection", "list", `{"data":{"type":"apps","id":"1"}}`, "must be an array"},
		{"wrong type", "one", `{"data":{"type":"builds","id":"1"}}`, `the spec says "apps"`},
		{"missing id", "list", `{"data":[{"type":"apps"}]}`, `data[0] lacks "id"`},
		{"bad included", "one", `{"data":{"type":"apps","id":"1"},"included":[{"id":"x"}]}`, `included[0] lacks "type"`},
		{"error without status", "one", `{"errors":[{"code":"X"}]}`, `errors[0] lacks "status"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := one
			if tt.op == "list" {
				op = list
			}
			got := strings.Join(fixtureProblems(op, []byte(tt.body)), "; ")
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("problems = %q, want %q", got, tt.want)
			}
		})
	}
	rec := &recTB{TB: t}
	ValidateFixture(rec, "no_such_op", []byte(`{}`))
	if !strings.Contains(rec.messages(), "unknown operation") {
		t.Fatalf("unknown op not reported: %q", rec.messages())
	}
}
