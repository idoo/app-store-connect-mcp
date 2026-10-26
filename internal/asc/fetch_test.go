package asc

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestFetcher returns a Fetcher that may reach loopback servers (on any
// port) and trusts srv's TLS certificate. Everything else in the policy
// still applies.
func newTestFetcher(t *testing.T, srv *httptest.Server, opts FetchOptions) *Fetcher {
	t.Helper()
	f := NewFetcher(opts)
	f.allowLoopback = true
	tr, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected test transport %T", srv.Client().Transport)
	}
	f.transport.TLSClientConfig = tr.TLSClientConfig.Clone()
	return f
}

func TestFetchURLPolicy(t *testing.T) {
	f := NewFetcher(FetchOptions{})
	tests := []struct {
		name, method, url string
		header            http.Header
		want              string
	}{
		{"http scheme", "GET", "http://example.com/a", nil, "not https"},
		{"userinfo", "GET", "https://u:p@example.com/a", nil, "userinfo"},
		{"non-443 port", "GET", "https://example.com:8443/a", nil, "port 8443"},
		{"no host", "GET", "https:///a", nil, "missing host"},
		{"private 10/8", "GET", "https://10.1.2.3/a", nil, "not public"},
		{"loopback v4", "GET", "https://127.0.0.1/a", nil, "not public"},
		{"link-local metadata", "GET", "https://169.254.169.254/latest", nil, "not public"},
		{"loopback v6", "GET", "https://[::1]/a", nil, "not public"},
		{"mapped private v6", "GET", "https://[::ffff:10.0.0.1]/a", nil, "not public"},
		{"delete method", "DELETE", "https://example.com/a", nil, "method not allowed"},
		{"authorization header", "GET", "https://example.com/a", http.Header{"Authorization": {"Bearer x"}}, "Authorization"},
		{"cookie header", "PUT", "https://example.com/a", http.Header{"Cookie": {"a=b"}}, "Cookie"},
		{"host header", "PUT", "https://example.com/a", http.Header{"Host": {"evil"}}, "Host"},
		{"proxy header", "PUT", "https://example.com/a", http.Header{"Proxy-Authorization": {"x"}}, "Proxy-Authorization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), tt.method, tt.url, tt.header, nil, 0)
			if !errors.Is(err, ErrURLPolicy) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want url policy error containing %q", err, tt.want)
			}
		})
	}
}

func TestFetchErrorsHideQuery(t *testing.T) {
	f := NewFetcher(FetchOptions{})
	_, err := f.Fetch(context.Background(), "GET", "http://example.com/a?X-Amz-Signature=secret", nil, nil, 0)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v, want refusal without the query", err)
	}
}

func TestDialControl(t *testing.T) {
	f := NewFetcher(FetchOptions{})
	tests := []struct {
		addr string
		ok   bool
	}{
		{"17.253.144.10:443", true},
		{"[2620:149:af0::10]:443", true},
		{"10.0.0.1:443", false},
		{"172.16.5.4:443", false},
		{"192.168.1.1:443", false},
		{"127.0.0.1:443", false},
		{"169.254.169.254:443", false},
		{"[::1]:443", false},
		{"[fe80::1%en0]:443", false},
		{"[fc00::1]:443", false},
		{"[::ffff:127.0.0.1]:443", false},
		{"0.0.0.0:443", false},
		{"224.0.0.1:443", false},
		{"100.64.0.1:443", false},
		{"[64:ff9b::a00:1]:443", false},
		{"not-an-ip:443", false},
	}
	for _, tt := range tests {
		err := f.dialControl("tcp", tt.addr, nil)
		if (err == nil) != tt.ok {
			t.Errorf("dialControl(%s) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrURLPolicy) {
			t.Errorf("dialControl(%s) error %v is not ErrURLPolicy", tt.addr, err)
		}
	}
}

// TestFetchDialRefusesResolvedLoopback shows the dial-time check: the URL is
// a hostname (passes the URL check) that resolves to loopback.
func TestFetchDialRefusesResolvedLoopback(t *testing.T) {
	f := NewFetcher(FetchOptions{})
	_, err := f.Fetch(context.Background(), "GET", "https://localhost/a", nil, nil, 0)
	if !errors.Is(err, ErrURLPolicy) || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("err = %v, want dial refusal", err)
	}
}

func TestFetchSuccessAndLimits(t *testing.T) {
	var got http.Header
	var gotBody []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.Bytes()
		w.Header().Set("X-Test", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()
	f := newTestFetcher(t, srv, FetchOptions{UserAgent: "app-store-connect-mcp/test"})

	resp, err := f.Fetch(context.Background(), "PUT", srv.URL+"/part?sig=1", http.Header{"Content-Type": {"application/octet-stream"}}, bytes.NewReader([]byte("part")), 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.Status != http.StatusAccepted || string(resp.Body) != "hello world" || resp.Header.Get("X-Test") != "1" {
		t.Fatalf("resp = %+v", resp)
	}
	if got.Get("Authorization") != "" || got.Get("User-Agent") != "app-store-connect-mcp/test" || got.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("request headers = %v", got)
	}
	if string(gotBody) != "part" {
		t.Fatalf("request body = %q", gotBody)
	}

	if _, err := f.Fetch(context.Background(), "GET", srv.URL+"/x", nil, nil, 5); err == nil || !strings.Contains(err.Error(), "exceeds 5 bytes") {
		t.Fatalf("max bytes: err = %v", err)
	}
	if _, err := f.Fetch(context.Background(), "GET", srv.URL+"/x", nil, nil, 11); err != nil {
		t.Fatalf("exact max bytes: %v", err)
	}
}

func TestFetchRedirectPolicy(t *testing.T) {
	tests := []struct {
		name, location, want string
	}{
		{"to private IP", "https://10.0.0.1/x", "not public"},
		{"to metadata", "https://169.254.169.254/x", "not public"},
		{"to http", "http://example.com/x", "not https"},
		{"to userinfo", "https://a:b@example.com/x", "userinfo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.location, http.StatusFound)
			}))
			defer srv.Close()
			f := newTestFetcher(t, srv, FetchOptions{})
			_, err := f.Fetch(context.Background(), "GET", srv.URL+"/start", nil, nil, 0)
			if !errors.Is(err, ErrURLPolicy) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want redirect refusal containing %q", err, tt.want)
			}
		})
	}
}

func TestFetchRedirectLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()
	f := newTestFetcher(t, srv, FetchOptions{})
	_, err := f.Fetch(context.Background(), "GET", srv.URL+"/loop", nil, nil, 0)
	if !errors.Is(err, ErrURLPolicy) || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("err = %v, want redirect limit", err)
	}
}
