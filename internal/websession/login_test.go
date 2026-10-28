package websession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeWidgetKey = "fakewidgetkey123"
	fakeSCNT      = "scnt-secret-value"
	fakeSessionID = "session-id-secret"
	fakeMyacinfo  = "myacinfo-secret-cookie"
	fakeDES       = "des-trust-secret"
	fakeDQSID     = "dqsid-secret"
)

// fakeApple serves idmsa.apple.com and appstoreconnect.apple.com from one
// TLS test server; requests arrive via hostRewriter with X-Test-Host.
type fakeApple struct {
	t        *testing.T
	password string
	account  string
	// twoFactor: "" none, "device" trusted devices, "nodevices" SMS only.
	twoFactor  string
	code       string
	logoutKey  bool // HEAD /logout redirects with the widget key
	initStatus int  // non-zero: signin/init fails with this status
	salt       []byte

	mu             sync.Mutex
	srv            *srpTestServer
	signedIn       bool
	verified       bool
	smsPhone       int
	calls          []string
	cookieOnLogout bool
}

func (f *fakeApple) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeApple) serviceError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Echo secrets in Apple's message to prove Login never repeats it.
	_ = json.NewEncoder(w).Encode(map[string]any{"serviceErrors": []map[string]string{{
		"code": code, "message": "bad " + f.password + " " + f.code + " " + fakeSCNT,
	}}})
}

func (f *fakeApple) checkSession(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("scnt") != fakeSCNT || r.Header.Get("X-Apple-ID-Session-Id") != fakeSessionID ||
		r.Header.Get("X-Apple-Widget-Key") != fakeWidgetKey {
		f.t.Errorf("%s %s: missing continuation headers", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	return true
}

func (f *fakeApple) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Test-Host")
	key := host + " " + r.Method + " " + r.URL.Path
	f.record(key)
	body, _ := io.ReadAll(r.Body)
	switch key {
	case "appstoreconnect.apple.com HEAD /logout":
		if r.Header.Get("Cookie") != "" {
			f.cookieOnLogout = true
		}
		if f.logoutKey {
			w.Header().Set("Location", "https://idmsa.apple.com/appleauth/signout?widgetKey="+fakeWidgetKey)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "appstoreconnect.apple.com GET /olympus/v1/app/config":
		_ = json.NewEncoder(w).Encode(map[string]string{"authServiceKey": fakeWidgetKey})
	case "idmsa.apple.com GET /appleauth/auth/signin":
		w.Header().Set("X-Apple-HC-Bits", "8")
		w.Header().Set("X-Apple-HC-Challenge", "abcdef0123")
		w.WriteHeader(http.StatusOK)
	case "idmsa.apple.com POST /appleauth/auth/signin/init":
		if r.Header.Get("X-Apple-Widget-Key") != fakeWidgetKey {
			f.t.Errorf("init: widget key %q", r.Header.Get("X-Apple-Widget-Key"))
		}
		if f.initStatus != 0 {
			w.Header().Set("Retry-After", "120")
			f.serviceError(w, f.initStatus, "-1")
			return
		}
		var in struct {
			AccountName string   `json:"accountName"`
			Protocols   []string `json:"protocols"`
			A           string   `json:"a"`
		}
		_ = json.Unmarshal(body, &in)
		f.mu.Lock()
		f.srv = newSRPTestServer(f.t, f.password, f.salt, 1000, "s2k_fo", 5)
		B := f.srv.B.Bytes()
		f.mu.Unlock()
		w.Header().Set("scnt", fakeSCNT)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iteration": 1000, "salt": base64.StdEncoding.EncodeToString(f.salt), "protocol": "s2k_fo",
			"b": base64.StdEncoding.EncodeToString(B), "c": "challenge-token",
		})
		f.mu.Lock()
		f.calls = append(f.calls, "A="+in.A)
		f.mu.Unlock()
	case "idmsa.apple.com POST /appleauth/auth/signin/complete":
		if r.URL.Query().Get("isRememberMeEnabled") != "true" {
			f.t.Errorf("complete: isRememberMeEnabled=%q", r.URL.Query().Get("isRememberMeEnabled"))
		}
		if hc := r.Header.Get("X-Apple-HC"); !strings.Contains(hc, ":abcdef0123::") {
			f.t.Errorf("complete: hashcash %q", hc)
		}
		if r.Header.Get("scnt") != fakeSCNT {
			f.t.Errorf("complete: scnt not propagated")
		}
		var in struct {
			AccountName string `json:"accountName"`
			M1, M2      string
			C           string `json:"c"`
			RememberMe  bool   `json:"rememberMe"`
		}
		_ = json.Unmarshal(body, &in)
		var aB64 string
		f.mu.Lock()
		for _, c := range f.calls {
			if v, ok := strings.CutPrefix(c, "A="); ok {
				aB64 = v
			}
		}
		srv := f.srv
		f.mu.Unlock()
		aBytes, _ := base64.StdEncoding.DecodeString(aB64)
		wantM1, wantM2 := srv.expected(f.account, aBytes)
		gotM1, _ := base64.StdEncoding.DecodeString(in.M1)
		gotM2, _ := base64.StdEncoding.DecodeString(in.M2)
		if string(gotM1) != string(wantM1) {
			f.serviceError(w, http.StatusUnauthorized, "-20101")
			return
		}
		if string(gotM2) != string(wantM2) || in.C != "challenge-token" || !in.RememberMe {
			f.t.Errorf("complete: bad m2/c/rememberMe")
		}
		http.SetCookie(w, &http.Cookie{Name: "myacinfo", Value: fakeMyacinfo, Domain: "apple.com", Path: "/", Secure: true, HttpOnly: true})
		w.Header().Set("scnt", fakeSCNT)
		w.Header().Set("X-Apple-ID-Session-Id", fakeSessionID)
		if f.twoFactor != "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"authType":"hsa2"}`))
			return
		}
		f.mu.Lock()
		f.signedIn = true
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"authType":"hsa2"}`))
	case "idmsa.apple.com GET /appleauth/auth":
		if !f.checkSession(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"noTrustedDevices": f.twoFactor == "nodevices",
			"trustedPhoneNumbers": []map[string]any{
				{"id": 1, "pushMode": "sms", "numberWithDialCode": "+1 (•••) •••-••11"},
				{"id": 2, "pushMode": "sms", "numberWithDialCode": "+46 •• ••• •• 22"},
			},
			"securityCode": map[string]any{"length": 6},
		})
	case "idmsa.apple.com PUT /appleauth/auth/verify/phone":
		if !f.checkSession(w, r) {
			return
		}
		var in struct {
			PhoneNumber struct{ ID int } `json:"phoneNumber"`
			Mode        string           `json:"mode"`
		}
		_ = json.Unmarshal(body, &in)
		f.mu.Lock()
		f.smsPhone = in.PhoneNumber.ID
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case "idmsa.apple.com POST /appleauth/auth/verify/trusteddevice/securitycode",
		"idmsa.apple.com POST /appleauth/auth/verify/phone/securitycode":
		if !f.checkSession(w, r) {
			return
		}
		var in struct {
			SecurityCode struct{ Code string } `json:"securityCode"`
			PhoneNumber  struct{ ID int }      `json:"phoneNumber"`
		}
		_ = json.Unmarshal(body, &in)
		if in.SecurityCode.Code != f.code {
			f.serviceError(w, http.StatusBadRequest, "-21669")
			return
		}
		if strings.Contains(r.URL.Path, "/phone/") && in.PhoneNumber.ID != f.smsPhone {
			f.t.Errorf("phone code for phone %d, SMS sent to %d", in.PhoneNumber.ID, f.smsPhone)
		}
		f.mu.Lock()
		f.verified = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "idmsa.apple.com GET /appleauth/auth/2sv/trust":
		if !f.checkSession(w, r) {
			return
		}
		f.mu.Lock()
		ok := f.verified
		if ok {
			f.signedIn = true
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "DES58b0abc", Value: fakeDES, Domain: "idmsa.apple.com", Path: "/", MaxAge: 3600, Secure: true, HttpOnly: true})
		w.WriteHeader(http.StatusNoContent)
	case "appstoreconnect.apple.com GET /olympus/v1/session":
		c, err := r.Cookie("myacinfo")
		f.mu.Lock()
		ok := f.signedIn
		f.mu.Unlock()
		if err != nil || c.Value != fakeMyacinfo || !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "dqsid", Value: fakeDQSID, Path: "/", Secure: true, HttpOnly: true})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user":     map[string]any{"emailAddress": f.account},
			"provider": map[string]any{"providerId": 123, "publicProviderId": "pp-1", "name": "Acme AB"},
		})
	default:
		f.t.Errorf("unexpected request %s", key)
		w.WriteHeader(http.StatusNotFound)
	}
}

// hostRewriter sends every request to the test server, keeping the original
// URL (and so the cookie jar's view) on Apple's hosts.
type hostRewriter struct {
	base http.RoundTripper
	addr string
}

func (h hostRewriter) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Test-Host", r.URL.Hostname())
	r2.URL.Scheme = "https"
	r2.URL.Host = h.addr
	r2.Host = h.addr
	return h.base.RoundTrip(r2)
}

func newFakeApple(t *testing.T, twoFactor string) (*fakeApple, *http.Client) {
	t.Helper()
	f := &fakeApple{
		t: t, password: "Corr3ct-Horse!", account: "dev@example.com", twoFactor: twoFactor,
		code: "123456", logoutKey: true, salt: []byte("salt-1234567890"),
	}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	hc := &http.Client{
		Transport: hostRewriter{base: srv.Client().Transport, addr: srv.Listener.Addr().String()},
		Timeout:   10 * time.Second,
	}
	return f, hc
}

var testNow = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

func loginOpts(f *fakeApple, hc *http.Client, code func(context.Context, TwoFactor) (string, error)) LoginOptions {
	return LoginOptions{
		AppleID: f.account, Password: f.password, HTTP: hc, Code: code,
		Now: func() time.Time { return testNow },
	}
}

func cookieByName(s *Session, name string) *Cookie {
	for i := range s.Cookies {
		if s.Cookies[i].Name == name {
			return &s.Cookies[i]
		}
	}
	return nil
}

func checkSession(t *testing.T, s *Session) {
	t.Helper()
	if s.Source != "login" || !s.CreatedAt.Equal(testNow) || s.AppleID != "dev@example.com" ||
		s.ProviderID != 123 || s.ProviderName != "Acme AB" {
		t.Fatalf("session fields: %v provider=%d %q", s, s.ProviderID, s.ProviderName)
	}
	my := cookieByName(s, "myacinfo")
	if my == nil || my.Domain != ".apple.com" || my.Path != "/" || !my.Secure || !my.HTTPOnly || my.Value != fakeMyacinfo {
		t.Fatalf("myacinfo cookie: %+v", my)
	}
	dq := cookieByName(s, "dqsid")
	if dq == nil || dq.Domain != "appstoreconnect.apple.com" {
		t.Fatalf("dqsid cookie: %+v", dq)
	}
	if h := s.CookieHeader("appstoreconnect.apple.com", "/iris/v1/apps", testNow); !strings.Contains(h, "myacinfo=") || !strings.Contains(h, "dqsid=") {
		t.Fatal("cookie header for ASC lacks session cookies")
	}
}

func TestLoginWithoutTwoFactor(t *testing.T) {
	f, hc := newFakeApple(t, "")
	called := false
	s, err := Login(context.Background(), loginOpts(f, hc, func(context.Context, TwoFactor) (string, error) {
		called = true
		return "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("prompted for a code without 2FA")
	}
	if f.cookieOnLogout {
		t.Fatal("HEAD /logout carried cookies")
	}
	checkSession(t, s)
}

func TestLoginTrustedDeviceCode(t *testing.T) {
	f, hc := newFakeApple(t, "device")
	var seen []TwoFactor
	s, err := Login(context.Background(), loginOpts(f, hc, func(_ context.Context, c TwoFactor) (string, error) {
		seen = append(seen, c)
		return " 123456 ", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Kind != "trusted_device" || seen[0].CodeLength != 6 || len(seen[0].Phones) != 2 ||
		seen[0].Phones[1] != "+46 •• ••• •• 22" {
		t.Fatalf("challenges: %+v", seen)
	}
	checkSession(t, s)
	des := cookieByName(s, "DES58b0abc")
	if des == nil || des.Domain != ".idmsa.apple.com" || des.Expires.IsZero() {
		t.Fatalf("trust cookie not kept: %+v", des)
	}
}

func TestLoginSMSPath(t *testing.T) {
	f, hc := newFakeApple(t, "device")
	f.code = "654321"
	var kinds []string
	s, err := Login(context.Background(), loginOpts(f, hc, func(_ context.Context, c TwoFactor) (string, error) {
		kinds = append(kinds, c.Kind)
		if c.Kind == "trusted_device" {
			return "sms:2", nil
		}
		return "654321", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "trusted_device,sms" || f.smsPhone != 2 {
		t.Fatalf("kinds %v, sms phone %d", kinds, f.smsPhone)
	}
	checkSession(t, s)
}

func TestLoginNoTrustedDevicesUsesSMS(t *testing.T) {
	f, hc := newFakeApple(t, "nodevices")
	var kinds []string
	_, err := Login(context.Background(), loginOpts(f, hc, func(_ context.Context, c TwoFactor) (string, error) {
		kinds = append(kinds, c.Kind)
		return f.code, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "sms" || f.smsPhone != 1 {
		t.Fatalf("kinds %v, sms phone %d", kinds, f.smsPhone)
	}
}

func TestLoginWidgetKeyFallsBackToConfig(t *testing.T) {
	f, hc := newFakeApple(t, "")
	f.logoutKey = false
	if _, err := Login(context.Background(), loginOpts(f, hc, nil)); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range f.calls {
		found = found || c == "appstoreconnect.apple.com GET /olympus/v1/app/config"
	}
	if !found {
		t.Fatal("config endpoint not consulted")
	}
}

func loginErrKind(t *testing.T, err error) LoginErrorKind {
	t.Helper()
	var le *LoginError
	if !errors.As(err, &le) {
		t.Fatalf("want *LoginError, got %T: %v", err, err)
	}
	return le.Kind
}

func TestLoginErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeApple, o *LoginOptions)
		want  LoginErrorKind
	}{
		{"wrong password", func(f *fakeApple, o *LoginOptions) { o.Password = "wrong-" + f.password }, LoginWrongPassword},
		{"rejected code", func(f *fakeApple, o *LoginOptions) {
			f.twoFactor = "device"
			o.Code = func(context.Context, TwoFactor) (string, error) { return "999999", nil }
		}, LoginCodeRejected},
		{"rate limited", func(f *fakeApple, _ *LoginOptions) { f.initStatus = http.StatusServiceUnavailable }, LoginRateLimited},
		{"no prompt", func(f *fakeApple, o *LoginOptions) { f.twoFactor = "device"; o.Code = nil }, LoginTwoFactorUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, hc := newFakeApple(t, "")
			o := loginOpts(f, hc, nil)
			tc.setup(f, &o)
			_, err := Login(context.Background(), o)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := loginErrKind(t, err); got != tc.want {
				t.Fatalf("kind %s, want %s (%v)", got, tc.want, err)
			}
			msg := err.Error()
			for _, secret := range []string{f.password, o.Password, "999999", f.code, fakeSCNT, fakeSessionID, fakeMyacinfo, "bad "} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error %q leaks a secret", msg)
				}
			}
			if tc.want == LoginRateLimited && !strings.Contains(msg, "wait 120 seconds") {
				t.Fatalf("rate-limit error does not say how long to wait: %q", msg)
			}
		})
	}
}

func TestLoginRetriesRejectedCode(t *testing.T) {
	t.Run("wrong then right", func(t *testing.T) {
		f, hc := newFakeApple(t, "device")
		var seen []TwoFactor
		s, err := Login(context.Background(), loginOpts(f, hc, func(_ context.Context, c TwoFactor) (string, error) {
			seen = append(seen, c)
			if len(seen) == 1 {
				return "000000", nil
			}
			return f.code, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen[0].Rejected || !seen[1].Rejected || seen[1].Kind != "trusted_device" {
			t.Fatalf("challenges: %+v", seen)
		}
		checkSession(t, s)
	})
	t.Run("sms after rejection", func(t *testing.T) {
		f, hc := newFakeApple(t, "device")
		var seen []TwoFactor
		_, err := Login(context.Background(), loginOpts(f, hc, func(_ context.Context, c TwoFactor) (string, error) {
			seen = append(seen, c)
			switch len(seen) {
			case 1:
				return "000000", nil
			case 2:
				return "sms:1", nil
			default:
				return f.code, nil
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 3 || !seen[1].Rejected || seen[2].Kind != "sms" || f.smsPhone != 1 {
			t.Fatalf("challenges: %+v, sms phone %d", seen, f.smsPhone)
		}
	})
	t.Run("three wrong", func(t *testing.T) {
		f, hc := newFakeApple(t, "device")
		calls := 0
		_, err := Login(context.Background(), loginOpts(f, hc, func(context.Context, TwoFactor) (string, error) {
			calls++
			return "000000", nil
		}))
		if got := loginErrKind(t, err); got != LoginCodeRejected || calls != 3 {
			t.Fatalf("kind %s after %d prompts", got, calls)
		}
		if strings.Contains(err.Error(), "000000") {
			t.Fatal("error leaks the code")
		}
	})
}

func TestLoginRequiresCredentials(t *testing.T) {
	if _, err := Login(context.Background(), LoginOptions{Password: "x"}); err == nil {
		t.Fatal("missing Apple ID accepted")
	}
	if _, err := Login(context.Background(), LoginOptions{AppleID: "a@b.c"}); err == nil {
		t.Fatal("missing password accepted")
	}
}
