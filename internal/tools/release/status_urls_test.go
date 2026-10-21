package release

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// fakeNet resolves hosts from a table and dials the TLS test server for
// any public address, recording the dialed addresses.
type fakeNet struct {
	mu     sync.Mutex
	dialed []string
}

// net returns a urlNet that resolves hosts and dials srv.
func (f *fakeNet) net(srv *httptest.Server, hosts map[string]string) urlNet {
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return urlNet{
		lookup: func(_ context.Context, host string) ([]net.IP, error) {
			ip, ok := hosts[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			return []net.IP{net.ParseIP(ip)}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			f.mu.Lock()
			f.dialed = append(f.dialed, addr)
			f.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
		// The test certificate is for example.com; pin it for every host.
		tls: &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12},
	}
}

// CP8: private, loopback and link-local targets are refused before any
// connection, also when reached via a redirect; a public host is probed.
func TestCheckURLsRefusesPrivate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "https://intranet.example.com/", http.StatusFound)
		case "/to-loopback":
			http.Redirect(w, r, "https://127.0.0.1/admin", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.com/", http.StatusFound)
		case "/nohead":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	fn := &fakeNet{}
	n := fn.net(srv, map[string]string{
		"example.com":          "93.184.216.34",
		"intranet.example.com": "10.0.0.7",
		"meta.example.com":     "169.254.169.254",
	})
	ctx := context.Background()
	for raw, want := range map[string]error{
		"https://127.0.0.1/":              nil,
		"https://10.1.2.3/support":        nil,
		"https://169.254.169.254/latest":  nil,
		"https://[::1]/":                  nil,
		"https://meta.example.com/":       nil,
		"https://example.com/to-private":  nil,
		"https://example.com/to-loopback": nil,
		"http://example.com/":             errNotHTTPS,
		"https://example.com/to-http":     errNotHTTPS,
		"https://user:pw@example.com/":    errNotHTTPS,
	} {
		if want == nil {
			want = errPrivateAddr
		}
		if _, err := n.probe(ctx, raw); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", raw, err, want)
		}
	}
	for _, a := range fn.dialed {
		if !strings.HasPrefix(a, "93.184.216.34:") {
			t.Errorf("dialed a non-public address %s", a)
		}
	}
	if st, err := n.probe(ctx, "https://example.com/ok"); err != nil || st != 200 {
		t.Errorf("public: %d, %v", st, err)
	}
	if st, err := n.probe(ctx, "https://example.com/nohead"); err != nil || st != 200 {
		t.Errorf("GET fallback: %d, %v", st, err)
	}
	if _, err := n.probe(ctx, "https://example.com/loop"); err == nil || urlReason(err) != "too many redirects" {
		t.Errorf("redirect loop: %v", err)
	}
}

// CP8: check_urls reports host and status only; failures warn, never block.
func TestReleaseStatusCheckURLs(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/privacy") {
			http.Redirect(w, r, "https://intranet.example.com/x", http.StatusFound)
		}
	}))
	defer srv.Close()
	n := (&fakeNet{}).net(srv, map[string]string{"jvcr.se": "93.184.216.34", "intranet.example.com": "192.168.1.5"})
	ctx := context.WithValue(context.Background(), urlNetKey{}, n)
	k := setup(t, readyFixture())
	res := tooltest.MustOK(t, mustTool(t, "release_status").Call(ctx, k.Env, json.RawMessage(`{"app":"Hjul","check_urls":true}`)))
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("release_status sent %d mutations", len(m))
	}
	out := tooltest.Decode[statusOut](t, res)
	c := checkByID(t, out, "urls")
	want := []string{"privacy jvcr.se: refused: private address", "support jvcr.se: 200"}
	if c.Status != statusWarn || !slices.Equal(c.Items, want) || !ready(out) {
		t.Errorf("urls = %+v ready=%v", c, ready(out))
	}
	out, _ = call(t, setup(t, readyFixture()), `{"app":"Hjul"}`)
	if slices.ContainsFunc(out.Checks, func(c check) bool { return c.ID == "urls" }) {
		t.Error("urls checked without check_urls")
	}
}

// publicIP keeps the shared policy's verdict for net.IP forms.
func TestPublicIPNetForms(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.216.34": true, "::ffff:93.184.216.34": true, "10.0.0.7": false,
		"::1": false, "169.254.169.254": false, "100.64.0.1": false,
	} {
		if got := publicIP(net.ParseIP(s)); got != want {
			t.Errorf("publicIP(%s) = %v", s, got)
		}
	}
	if publicIP(nil) {
		t.Error("publicIP(nil) = true")
	}
}
