package websession

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// memStore is an in-memory Store counting saves.
type memStore struct {
	mu    sync.Mutex
	m     map[string]Session
	saves int
}

func newMemStore() *memStore { return &memStore{m: map[string]Session{}} }

func (s *memStore) Load(p string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[p]
	if !ok {
		return nil, ErrNoSession
	}
	v.Cookies = append([]Cookie(nil), v.Cookies...)
	return &v, nil
}

func (s *memStore) Save(p string, v *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *v
	cp.Cookies = append([]Cookie(nil), v.Cookies...)
	s.m[p] = cp
	s.saves++
	return nil
}

func (s *memStore) Delete(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, p)
	return nil
}

func (s *memStore) value(p, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.m[p].Cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func srcSession() *Session {
	return &Session{AppleID: "a@b.c", Cookies: []Cookie{
		{Name: "myacinfo", Value: "M1", Domain: ".apple.com", Path: "/"},
		{Name: "dqsid", Value: "D1", Domain: "appstoreconnect.apple.com", Path: "/"},
		{Name: "other", Value: "O1", Domain: "idmsa.apple.com", Path: "/"},
	}}
}

func irisReq(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, IrisBaseURL+"/v1/apps", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func respWith(req *http.Request, setCookies ...string) *http.Response {
	h := http.Header{}
	for _, c := range setCookies {
		h.Add("Set-Cookie", c)
	}
	return &http.Response{StatusCode: 200, Header: h, Request: req}
}

func TestSourceHeaders(t *testing.T) {
	st := newMemStore()
	_ = st.Save("p", srcSession())
	src := NewSource("p", st, nil)
	req := irisReq(t)
	if err := src.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Cookie"); got != "myacinfo=M1; dqsid=D1" {
		t.Errorf("Cookie %q", got)
	}
	if req.Header.Get("X-Requested-With") != "XMLHttpRequest" || req.Header.Get("X-CSRF-ITC") != "[asc-ui]" {
		t.Errorf("headers %v", req.Header)
	}
}

func TestSourceNoSession(t *testing.T) {
	src := NewSource("p", newMemStore(), nil)
	err := src.Authorize(context.Background(), irisReq(t))
	if !errors.Is(err, tool.ErrWebSessionRequired) || !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v", err)
	}
}

func TestSourceRotationAndThrottle(t *testing.T) {
	st := newMemStore()
	_ = st.Save("p", srcSession())
	st.saves = 0
	clk := &fakeClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	src := NewSource("p", st, clk.now)
	req := irisReq(t)
	if err := src.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Critical cookie: saved immediately.
	src.Observe(respWith(req, "myacinfo=M2; Domain=.apple.com; Path=/; Secure"))
	if st.saves != 1 || st.value("p", "myacinfo") != "M2" {
		t.Fatalf("saves %d myacinfo %q", st.saves, st.value("p", "myacinfo"))
	}
	// Non-critical within 30 s: cached, not saved.
	src.Observe(respWith(req, "itctx=I1; Path=/"))
	if st.saves != 1 {
		t.Fatalf("throttle: saves %d", st.saves)
	}
	r2 := irisReq(t)
	_ = src.Authorize(context.Background(), r2)
	if !strings.Contains(r2.Header.Get("Cookie"), "itctx=I1") {
		t.Fatalf("rotated cookie not used: %q", r2.Header.Get("Cookie"))
	}
	// Unchanged value: no save even after the throttle window.
	clk.add(31 * time.Second)
	src.Observe(respWith(req, "itctx=I1; Path=/"))
	if st.saves != 1 {
		t.Fatalf("unchanged saved: %d", st.saves)
	}
	src.Observe(respWith(req, "itctx=I2; Path=/"))
	if st.saves != 2 || st.value("p", "itctx") != "I2" {
		t.Fatalf("after window: saves %d itctx %q", st.saves, st.value("p", "itctx"))
	}
	// dqsid deletion is critical.
	src.Observe(respWith(req, "dqsid=; Max-Age=0; Path=/"))
	if st.saves != 3 || st.value("p", "dqsid") != "" {
		t.Fatalf("delete: saves %d", st.saves)
	}
	// Foreign hosts are ignored.
	foreign, _ := http.NewRequest(http.MethodGet, "https://evil.example.com/", nil)
	src.Observe(respWith(foreign, "myacinfo=EVIL; Path=/"))
	if st.value("p", "myacinfo") != "M2" {
		t.Fatal("foreign Set-Cookie accepted")
	}
}

func TestSourceExpired(t *testing.T) {
	st := newMemStore()
	_ = st.Save("p", srcSession())
	src := NewSource("p", st, nil)
	if err := src.Authorize(context.Background(), irisReq(t)); err != nil {
		t.Fatal(err)
	}
	src.Expired()
	err := src.Authorize(context.Background(), irisReq(t))
	if !errors.Is(err, tool.ErrWebSessionRequired) || !errors.Is(err, ErrExpired) || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("same session after expiry: %v", err)
	}
	// The user signs in again in a terminal: the new session works.
	fresh := srcSession()
	fresh.Cookies[0].Value = "M9"
	_ = st.Save("p", fresh)
	req := irisReq(t)
	if err := src.Authorize(context.Background(), req); err != nil {
		t.Fatalf("fresh session: %v", err)
	}
	if !strings.Contains(req.Header.Get("Cookie"), "myacinfo=M9") {
		t.Fatalf("Cookie %q", req.Header.Get("Cookie"))
	}
}

func TestSourceExpiredCookies(t *testing.T) {
	st := newMemStore()
	s := srcSession()
	for i := range s.Cookies {
		s.Cookies[i].Expires = time.Unix(1, 0)
	}
	_ = st.Save("p", s)
	err := NewSource("p", st, nil).Authorize(context.Background(), irisReq(t))
	if !errors.Is(err, ErrExpired) || !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestSourceConcurrent(t *testing.T) {
	st := newMemStore()
	_ = st.Save("p", srcSession())
	src := NewSource("p", st, nil)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for j := range 50 {
				req := irisReq(t)
				if err := src.Authorize(context.Background(), req); err != nil && !errors.Is(err, tool.ErrWebSessionRequired) {
					t.Error(err)
				}
				src.Observe(respWith(req, "myacinfo=M"+string(rune('a'+i))+"; Domain=.apple.com; Path=/"))
				if j%10 == 0 {
					src.Expired()
				}
			}
		})
	}
	wg.Wait()
}

// serverClient sends every request to srv, keeping the original URL host
// visible to cookie matching.
func serverClient(srv *httptest.Server) *http.Client {
	u, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = u.Scheme, u.Host
		resp, err := srv.Client().Transport.RoundTrip(r2)
		if resp != nil {
			resp.Request = r
		}
		return resp, err
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSelectProvider(t *testing.T) {
	var gotBody map[string]map[string]int64
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/olympus/v1/session" {
			http.Error(w, "bad", 404)
			return
		}
		gotHeaders = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		if !strings.Contains(r.Header.Get("Cookie"), "myacinfo=M1") {
			w.WriteHeader(401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "dqsid", Value: "D2", Path: "/", Domain: "appstoreconnect.apple.com"})
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := srcSession()
	if err := SelectProvider(context.Background(), serverClient(srv), s, 777, time.Now()); err != nil {
		t.Fatal(err)
	}
	if gotBody["provider"]["providerId"] != 777 {
		t.Errorf("body %v", gotBody)
	}
	if gotHeaders.Get("X-Requested-With") != "olympus-ui" || gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", gotHeaders)
	}
	if s.ProviderID != 777 {
		t.Errorf("ProviderID %d", s.ProviderID)
	}
	if h := s.CookieHeader("appstoreconnect.apple.com", "/", time.Now()); !strings.Contains(h, "dqsid=D2") || strings.Contains(h, "dqsid=D1") {
		t.Errorf("rotated cookie not captured: %q", h)
	}

	s2 := srcSession()
	s2.Cookies[0].Value = "BAD"
	if err := SelectProvider(context.Background(), serverClient(srv), s2, 777, time.Now()); !errors.Is(err, ErrExpired) {
		t.Fatalf("401: %v", err)
	}
	if err := SelectProvider(context.Background(), serverClient(srv), srcSession(), 0, time.Now()); err == nil {
		t.Fatal("team 0 accepted")
	}
}

func renewedSession(v string) *Session {
	s := srcSession()
	s.Cookies[0].Value = v
	s.Source = "browser"
	return s
}

func TestSourceRenewsWhenNoneStored(t *testing.T) {
	st := newMemStore()
	src := NewSource("p", st, nil)
	calls := 0
	src.SetRenewer(func(context.Context) (*Session, error) {
		calls++
		return renewedSession("R1"), nil
	})
	req := irisReq(t)
	if err := src.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Header.Get("Cookie"), "myacinfo=R1") {
		t.Fatalf("Cookie %q", req.Header.Get("Cookie"))
	}
	if st.value("p", "myacinfo") != "R1" || st.saves != 1 {
		t.Fatalf("not saved: %q saves=%d", st.value("p", "myacinfo"), st.saves)
	}
	if err := src.Authorize(context.Background(), irisReq(t)); err != nil || calls != 1 {
		t.Fatalf("second call: err=%v calls=%d", err, calls)
	}
}

func TestSourceRenewSingleFlight(t *testing.T) {
	st := newMemStore()
	src := NewSource("p", st, nil)
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	src.SetRenewer(func(context.Context) (*Session, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return renewedSession("R1"), nil
	})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errs <- src.Authorize(context.Background(), irisReq(t)) })
	}
	// The renewer runs without the Source mutex: Observe/Expired don't block.
	time.Sleep(20 * time.Millisecond)
	src.Observe(respWith(irisReq(t), "itctx=X; Domain=.apple.com; Path=/"))
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("renewer ran %d times", calls)
	}
}

func TestSourceRenewWaiterHonorsContext(t *testing.T) {
	src := NewSource("p", newMemStore(), nil)
	started, release := make(chan struct{}), make(chan struct{})
	src.SetRenewer(func(context.Context) (*Session, error) {
		close(started)
		<-release
		return renewedSession("R1"), nil
	})
	done := make(chan error, 1)
	go func() { done <- src.Authorize(context.Background(), irisReq(t)) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := src.Authorize(ctx, irisReq(t)); !errors.Is(err, context.Canceled) || !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("waiter: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSourceRenewBackoff(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	st := newMemStore()
	src := NewSource("p", st, clk.now)
	calls := 0
	fail := errors.New("the human needs to sign in")
	src.SetRenewer(func(context.Context) (*Session, error) {
		calls++
		if calls == 1 {
			return nil, fail
		}
		return renewedSession("R2"), nil
	})
	err := src.Authorize(context.Background(), irisReq(t))
	if !errors.Is(err, fail) || !errors.Is(err, ErrNoSession) || !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("first: %v", err)
	}
	clk.add(14 * time.Minute)
	if err := src.Authorize(context.Background(), irisReq(t)); !errors.Is(err, fail) || calls != 1 {
		t.Fatalf("during backoff: err=%v calls=%d", err, calls)
	}
	clk.add(time.Minute)
	if err := src.Authorize(context.Background(), irisReq(t)); err != nil || calls != 2 {
		t.Fatalf("after backoff: err=%v calls=%d", err, calls)
	}
	if st.value("p", "myacinfo") != "R2" {
		t.Fatal("renewed session not saved")
	}
}

func TestSourceRenewCancelledNoBackoff(t *testing.T) {
	src := NewSource("p", newMemStore(), nil)
	calls := 0
	src.SetRenewer(func(ctx context.Context) (*Session, error) {
		calls++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return renewedSession("R1"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := src.Authorize(ctx, irisReq(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if err := src.Authorize(context.Background(), irisReq(t)); err != nil || calls != 2 {
		t.Fatalf("after cancel: err=%v calls=%d", err, calls)
	}
}

func TestSourceExpiredThenRenew(t *testing.T) {
	st := newMemStore()
	_ = st.Save("p", srcSession())
	src := NewSource("p", st, nil)
	calls := 0
	src.SetRenewer(func(context.Context) (*Session, error) {
		calls++
		return renewedSession("R1"), nil
	})
	if err := src.Authorize(context.Background(), irisReq(t)); err != nil || calls != 0 {
		t.Fatalf("stored session: err=%v calls=%d", err, calls)
	}
	src.Expired()
	req := irisReq(t)
	if err := src.Authorize(context.Background(), req); err != nil || calls != 1 {
		t.Fatalf("after Expired: err=%v calls=%d", err, calls)
	}
	if !strings.Contains(req.Header.Get("Cookie"), "myacinfo=R1") || st.value("p", "myacinfo") != "R1" {
		t.Fatalf("Cookie %q stored %q", req.Header.Get("Cookie"), st.value("p", "myacinfo"))
	}
}

func TestSourceRenewNilSessionAndDisable(t *testing.T) {
	src := NewSource("p", newMemStore(), nil)
	src.SetRenewer(func(context.Context) (*Session, error) { return nil, nil })
	if err := src.Authorize(context.Background(), irisReq(t)); !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("nil session: %v", err)
	}
	src2 := NewSource("p", newMemStore(), nil)
	src2.SetRenewer(nil)
	if err := src2.Authorize(context.Background(), irisReq(t)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("disabled: %v", err)
	}
}
