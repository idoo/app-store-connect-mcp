package release

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// CP1: the pending iPhone Duo slot warns before 2027-04-01; after it, it
// blocks only when the spec can upload the slot.
func TestIPhoneDuoByDate(t *testing.T) {
	before := setup(t, readyFixture())
	out, _ := call(t, before, `{"app":"Hjul"}`)
	c := checkByID(t, out, "iphone_duo_screenshots")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "required for submissions from 2027-04-01") || !ready(out) {
		t.Errorf("before cutoff: %+v ready=%v", c, ready(out))
	}

	// The pinned spec (4.5.1) lacks APP_IPHONE_DUO: warn, never block.
	after := setup(t, readyFixture())
	after.Clock.Set(time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC))
	out, _ = call(t, after, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "iphone_duo_screenshots"); c.Status != statusWarn || !strings.Contains(c.Detail, "can't upload them yet") ||
		c.FixVia == nil || c.FixVia.Manual == "" || !ready(out) {
		t.Errorf("after cutoff, unknown to spec: %+v ready=%v", c, ready(out))
	}
	if specKnowsDisplay(after.Spec, "APP_IPHONE_DUO") || !specKnowsDisplay(after.Spec, "APP_IPHONE_67") {
		t.Error("specKnowsDisplay disagrees with the pinned spec")
	}

	s := &statusSnapshot{App: tool.App{ID: "1"}, Platform: tool.IOS, Now: time.Date(2027, 4, 2, 0, 0, 0, 0, time.UTC)}
	s.Uploadable = map[applemeta.DisplayType]bool{applemeta.AppIPhoneDuo: true}
	if c := checkPendingDisplays(s); c.Status != statusBlock || c.FixVia == nil || c.FixVia.Tool != "screenshots_plan" {
		t.Errorf("after cutoff, uploadable: %+v", c)
	}
	s.Uploadable = nil
	if c := checkPendingDisplays(s); c.Status != statusWarn {
		t.Errorf("after cutoff, not uploadable: %+v", c)
	}
	s.Screenshots = map[string][]screenshotSet{"en-US": {{Display: "APP_IPHONE_DUO", States: []string{"COMPLETE"}}}}
	if c := checkPendingDisplays(s); c.Status != statusOK {
		t.Errorf("covered = %+v", c)
	}
}

// cppRoutes serves one page whose editable version has an en-US
// localization with the given keywords.
func cppRoutes(t *testing.T, keywords ...string) func(k *tooltest.Kit) {
	return func(k *tooltest.Kit) {
		k.ASC.Handle("GET", "/v1/apps/{id}/appCustomProductPages", 200, doc(t, []jres{{
			Type: "appCustomProductPages", ID: "cpp1", Attrs: map[string]any{"name": "Racing"},
			Rels: map[string]any{"appCustomProductPageVersions": []string{"appCustomProductPageVersions:cv0", "appCustomProductPageVersions:cv1"}},
		}}, jres{Type: "appCustomProductPageVersions", ID: "cv0", Attrs: map[string]any{"state": "APPROVED"}},
			jres{Type: "appCustomProductPageVersions", ID: "cv1", Attrs: map[string]any{"state": "PREPARE_FOR_SUBMISSION"}}))
		k.ASC.Handle("GET", "/v1/appCustomProductPageVersions/{id}/appCustomProductPageLocalizations", 200,
			doc(t, []jres{{Type: "appCustomProductPageLocalizations", ID: "cl1", Attrs: map[string]any{"locale": "en-US"}}}))
		var kws []jres
		for _, kw := range keywords {
			kws = append(kws, jres{Type: "appKeywords", ID: kw})
		}
		k.ASC.Handle("GET", "/v1/appCustomProductPageLocalizations/{id}/searchKeywords", 200, doc(t, kws))
	}
}

// CP4: a page keyword missing from the version's keywords warns.
func TestCPPKeywordsDrift(t *testing.T) {
	f := readyFixture()
	f.cpp = cppRoutes(t, "Sail", "regatta")
	k := setup(t, f)
	out, _ := call(t, k, `{"app":"Hjul"}`)
	c := checkByID(t, out, "cpp_keywords")
	if c.Status != statusWarn || len(c.Items) != 1 || !strings.Contains(c.Items[0], `"regatta"`) || c.FixVia == nil || c.FixVia.Tool != "metadata_plan" {
		t.Errorf("drift = %+v", c)
	}
	if reqs := k.ASC.RequestsTo("GET", "/v1/appCustomProductPageVersions/{id}/appCustomProductPageLocalizations"); len(reqs) != 1 || !strings.Contains(reqs[0].Path, "/cv1/") {
		t.Errorf("version localizations read = %+v, want the editable cv1", reqs)
	}

	f.cpp = cppRoutes(t, "boat", "WIND")
	out, _ = call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "cpp_keywords"); c.Status != statusOK {
		t.Errorf("in sync = %+v", c)
	}
}

// CP4: a failing auxiliary GET warns "could not check", never blocks.
func TestCPPKeywordsAuxFailure(t *testing.T) {
	f := readyFixture()
	f.cpp = func(k *tooltest.Kit) {
		k.ASC.Handle("GET", "/v1/apps/{id}/appCustomProductPages", 500, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"boom"}]}`)
	}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	c := checkByID(t, out, "cpp_keywords")
	if c.Status != statusWarn || !strings.Contains(c.Detail, "could not check") || !ready(out) {
		t.Errorf("aux failure = %+v ready=%v", c, ready(out))
	}
}

// reviewKit serves submission states in order (the last repeats).
func reviewKit(t *testing.T, states ...string) *tooltest.Kit {
	t.Helper()
	k := writableKit(t)
	for _, st := range states {
		k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, doc(t, []jres{{
			Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"platform": "IOS", "state": st, "submittedDate": "2026-01-01T00:00:00Z"},
			Rels: map[string]any{"items": []string{"reviewSubmissionItems:i1"}},
		}}))
	}
	k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}/items", 200, doc(t, []jres{{Type: "reviewSubmissionItems", ID: "i1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}}))
	return k
}

// CP3: wait returns once the submission leaves review.
func TestReviewStatusWaitStateChange(t *testing.T) {
	k := reviewKit(t, "WAITING_FOR_REVIEW", "IN_REVIEW", "COMPLETE")
	calls := recordProgress(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{"wait":true}`))
	out := tooltest.Decode[reviewStatusOut](t, res)
	if out.Current == nil || out.Current.State != "COMPLETE" || out.Polls != 3 || out.WaitedSeconds != 60 {
		t.Fatalf("out = %+v", out)
	}
	if len(*calls) != 3 || !strings.Contains((*calls)[2].msg, "COMPLETE") || (*calls)[0].total != 120 {
		t.Errorf("progress = %+v", *calls)
	}
}

// CP3: at the timeout wait returns still_in_review with a resume step.
func TestReviewStatusWaitTimeout(t *testing.T) {
	k := reviewKit(t, "IN_REVIEW")
	recordProgress(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{"wait":true,"timeout_seconds":45}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "still_in_review" || !strings.Contains(fmt.Sprint(got["resume_hint"]), "webhooks") {
		t.Fatalf("out = %+v", got)
	}
	if k.Clock.Slept != 45*time.Second {
		t.Errorf("slept %v", k.Clock.Slept)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "release_review_status" || res.Next[0].Args["wait"] != true {
		t.Errorf("next = %+v", res.Next)
	}

	// Without wait nothing polls.
	k = reviewKit(t, "IN_REVIEW")
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{}`))
	if n := len(k.ASC.RequestsTo("GET", "/v1/reviewSubmissions")); n != 1 || k.Clock.Slept != 0 {
		t.Errorf("no-wait polled %d times, slept %v", n, k.Clock.Slept)
	}
}

// fakeNet resolves hosts from a table and dials the TLS test server for
// any public address, recording the dialed addresses.
type fakeNet struct {
	mu     sync.Mutex
	dialed []string
}

func (f *fakeNet) install(t *testing.T, srv *httptest.Server, hosts map[string]string) {
	t.Helper()
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	orig := urlNetFor
	t.Cleanup(func() { urlNetFor = orig })
	urlNetFor = func() urlNet {
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
	fn.install(t, srv, map[string]string{
		"example.com":          "93.184.216.34",
		"intranet.example.com": "10.0.0.7",
		"meta.example.com":     "169.254.169.254",
	})
	n := urlNetFor()
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
	(&fakeNet{}).install(t, srv, map[string]string{"jvcr.se": "93.184.216.34", "intranet.example.com": "192.168.1.5"})
	k := setup(t, readyFixture())
	out, _ := call(t, k, `{"app":"Hjul","check_urls":true}`)
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
