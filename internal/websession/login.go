// Apple ID sign-in for App Store Connect's web session. Protocol details
// follow rudrankriyam/App-Store-Connect-CLI (MIT): internal/web/auth.go,
// auth_twofactor_shared.go, auth_signin_errors.go, internal/appleauth.

package websession

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Apple hosts used by Login; tests may point them elsewhere.
var (
	idmsaBase = "https://idmsa.apple.com"
	ascBase   = ASCOrigin
)

// loginPublicWidgetKey is App Store Connect's public web widget key (not an
// account credential), used when discovery fails. Same value as rudrank's
// appStoreConnectWidgetKey, verified there 2026-09-11.
const loginPublicWidgetKey = "e0b80c3bf78523bfe80974d320935bfa30add02e1bff88ec2166c6bd5a706c42" // gitleaks:allow (public, ships in Apple's web page)

// loginBodyLimit bounds every response body Login reads.
const loginBodyLimit = 1 << 20

// loginFlow is one sign-in attempt's mutable state. Header values are
// secrets: never print them.
type loginFlow struct {
	hc        *http.Client
	jar       *loginJar
	widgetKey string
	scnt      string
	sessionID string
	now       func() time.Time
}

// Login signs in to App Store Connect with an Apple ID: SRP-6a password
// proof, Apple's hashcash, optional two-factor code via o.Code, then the
// App Store Connect olympus session. It returns the cookies the browser
// would hold. The password is used only to derive the SRP proof and is
// never stored, logged or included in errors. Failures from Apple are
// *LoginError.
func Login(ctx context.Context, o LoginOptions) (*Session, error) {
	appleID := strings.TrimSpace(o.AppleID)
	if appleID == "" {
		return nil, errLoginf("Apple ID is required")
	}
	if o.Password == "" {
		return nil, errLoginf("password is required")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	f, err := newLoginFlow(o.HTTP, now)
	if err != nil {
		return nil, err
	}

	f.widgetKey = f.discoverWidgetKey(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hashcash, err := f.hashcash(ctx)
	if err != nil {
		return nil, err
	}
	twoFactor, err := f.signin(ctx, appleID, o.Password, hashcash)
	if err != nil {
		return nil, err
	}
	if twoFactor {
		if err := f.twoFactor(ctx, o.Code); err != nil {
			return nil, err
		}
	}
	if err := f.openASCSession(ctx); err != nil {
		return nil, err
	}

	s := &Session{
		AppleID:   appleID,
		Cookies:   f.jar.collect(now()),
		CreatedAt: now(),
		Source:    "login",
	}
	plain := *f.hc
	plain.Jar = nil
	info, err := Validate(ctx, &plain, s, now())
	if err != nil {
		if errors.Is(err, ErrExpired) {
			return nil, &LoginError{Kind: LoginNoASCAccess, Stage: "validate"}
		}
		return nil, fmt.Errorf("websession: login: %w", err)
	}
	if info.AppleID != "" {
		s.AppleID = info.AppleID
	}
	s.ProviderID = info.Provider.ID
	s.ProviderName = info.Provider.Name
	return s, nil
}

// newLoginFlow copies hc (or builds a 30 s client) with a recording jar.
func newLoginFlow(hc *http.Client, now func() time.Time) (*loginFlow, error) {
	var c http.Client
	if hc != nil {
		c = *hc
	} else {
		c.Timeout = 30 * time.Second
	}
	inner := c.Jar
	if inner == nil {
		j, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("websession: login: cookie jar: %w", err)
		}
		inner = j
	}
	jar := &loginJar{inner: inner}
	c.Jar = jar
	return &loginFlow{hc: &c, jar: jar, now: now}, nil
}

// discoverWidgetKey finds the sign-in widget key: the signout redirect, then
// olympus app config, then the public constant. Discovery never fails Login.
func (f *loginFlow) discoverWidgetKey(ctx context.Context) string {
	if k := f.widgetKeyFromSignout(ctx); k != "" {
		return k
	}
	if k := f.widgetKeyFromConfig(ctx); k != "" {
		return k
	}
	return loginPublicWidgetKey
}

// widgetKeyFromSignout reads widgetKey from HEAD /logout's Location without
// cookies and without following it (following could sign the user out).
func (f *loginFlow) widgetKeyFromSignout(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ascBase+"/logout", nil)
	if err != nil {
		return ""
	}
	c := *f.hc
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return ""
	}
	idmsa, err := url.Parse(idmsaBase)
	if err != nil {
		return ""
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Scheme != "https" || !strings.EqualFold(loc.Host, idmsa.Host) ||
		loc.User != nil || loc.Path != "/appleauth/signout" {
		return ""
	}
	keys := loc.Query()["widgetKey"]
	if len(keys) != 1 {
		return ""
	}
	return validWidgetKey(keys[0])
}

// widgetKeyFromConfig reads authServiceKey from olympus app config.
func (f *loginFlow) widgetKeyFromConfig(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ascBase+"/olympus/v1/app/config?hostname=itunesconnect.apple.com", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	resp, body, err := f.do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	var cfg struct {
		AuthServiceKey string `json:"authServiceKey"`
	}
	if json.Unmarshal(body, &cfg) != nil {
		return ""
	}
	return validWidgetKey(cfg.AuthServiceKey)
}

// validWidgetKey accepts only a plain token (hex/alnum, bounded length).
func validWidgetKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" || len(k) > 128 {
		return ""
	}
	for _, r := range k {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' && r != '_' {
			return ""
		}
	}
	return k
}

// hashcash fetches Apple's proof-of-work challenge and solves it; "" when
// Apple does not ask for one.
func (f *loginFlow) hashcash(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		idmsaBase+"/appleauth/auth/signin?widgetKey="+url.QueryEscape(f.widgetKey), nil)
	if err != nil {
		return "", errLoginf("build hashcash request: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, body, err := f.do(req)
	if err != nil {
		return "", errLoginf("contact Apple sign-in: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", newLoginError("hashcash", resp, body)
	}
	bitsText := strings.TrimSpace(resp.Header.Get("X-Apple-HC-Bits"))
	challenge := strings.TrimSpace(resp.Header.Get("X-Apple-HC-Challenge"))
	if bitsText == "" || challenge == "" {
		return "", nil
	}
	bits, err := strconv.Atoi(bitsText)
	if err != nil || bits < 0 || bits > hashcashMaxBits || len(challenge) > 256 || strings.ContainsAny(challenge, ":\r\n") {
		return "", &LoginError{Kind: LoginUnexpected, Stage: "hashcash", Status: resp.StatusCode}
	}
	return makeHashcash(bits, challenge, f.now()), nil
}

// signinInitResponse is POST /appleauth/auth/signin/init's answer.
type signinInitResponse struct {
	Iteration int             `json:"iteration"`
	Salt      string          `json:"salt"`
	Protocol  string          `json:"protocol"`
	B         string          `json:"b"`
	C         json.RawMessage `json:"c"`
}

// signin runs SRP init+complete. It reports whether Apple wants 2FA (409).
func (f *loginFlow) signin(ctx context.Context, appleID, password, hashcash string) (bool, error) {
	client, err := newSRPClient(rand.Reader)
	if err != nil {
		return false, err
	}
	initBody, err := json.Marshal(map[string]any{
		"accountName": appleID,
		"protocols":   []string{"s2k", "s2k_fo"},
		"a":           base64.StdEncoding.EncodeToString(client.publicA()),
	})
	if err != nil {
		return false, errLoginf("encode sign-in request: %w", err)
	}
	resp, body, err := f.idmsaJSON(ctx, http.MethodPost, "/appleauth/auth/signin/init", initBody, "")
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, newLoginError("signin_init", resp, body)
	}
	var init signinInitResponse
	if json.Unmarshal(body, &init) != nil || len(init.C) == 0 || bytes.Equal(init.C, []byte("null")) {
		return false, &LoginError{Kind: LoginUnexpected, Stage: "signin_init", Status: resp.StatusCode}
	}
	salt, err1 := base64.StdEncoding.DecodeString(init.Salt)
	serverB, err2 := base64.StdEncoding.DecodeString(init.B)
	if err1 != nil || err2 != nil {
		return false, &LoginError{Kind: LoginUnexpected, Stage: "signin_init", Status: resp.StatusCode}
	}
	derived, err := derivePassword(init.Protocol, password, salt, init.Iteration)
	if err != nil {
		return false, err
	}
	m1, m2, err := client.proof(appleID, salt, serverB, derived)
	if err != nil {
		return false, err
	}

	completeBody, err := json.Marshal(struct {
		AccountName string          `json:"accountName"`
		RememberMe  bool            `json:"rememberMe"`
		M1          string          `json:"m1"`
		M2          string          `json:"m2"`
		C           json.RawMessage `json:"c"`
	}{appleID, true, base64.StdEncoding.EncodeToString(m1), base64.StdEncoding.EncodeToString(m2), init.C})
	if err != nil {
		return false, errLoginf("encode sign-in proof: %w", err)
	}
	resp, body, err = f.idmsaJSON(ctx, http.MethodPost,
		"/appleauth/auth/signin/complete?isRememberMeEnabled=true", completeBody, hashcash)
	if err != nil {
		return false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return false, nil
	case http.StatusConflict:
		return true, nil
	default:
		return false, newLoginError("signin_complete", resp, body)
	}
}

// openASCSession asks olympus for the App Store Connect session so ASC sets
// its own cookies (dqsid, itctx) in the jar.
func (f *loginFlow) openASCSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ascBase+"/olympus/v1/session", nil)
	if err != nil {
		return errLoginf("build session request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "olympus-ui")
	resp, body, err := f.do(req)
	if err != nil {
		return errLoginf("open App Store Connect session: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return &LoginError{Kind: LoginNoASCAccess, Stage: "olympus_session", Status: resp.StatusCode}
	default:
		return newLoginError("olympus_session", resp, body)
	}
}

// idmsaJSON sends a JSON request to idmsa with the widget key, the session
// continuation headers and an optional hashcash stamp.
func (f *loginFlow) idmsaJSON(ctx context.Context, method, path string, body []byte, hashcash string) (*http.Response, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, idmsaBase+path, r)
	if err != nil {
		return nil, nil, errLoginf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/javascript")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Apple-Widget-Key", f.widgetKey)
	if f.scnt != "" {
		req.Header.Set("scnt", f.scnt)
	}
	if f.sessionID != "" {
		req.Header.Set("X-Apple-ID-Session-Id", f.sessionID)
	}
	if hashcash != "" {
		req.Header.Set("X-Apple-HC", hashcash)
	}
	resp, data, err := f.do(req)
	if err != nil {
		return nil, nil, errLoginf("contact Apple sign-in: %w", err)
	}
	return resp, data, nil
}

// do sends req, reads a bounded body and remembers scnt/session id headers.
// Transport errors never contain request bodies.
func (f *loginFlow) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, loginBodyLimit))
	if err != nil {
		return nil, nil, err
	}
	if v := strings.TrimSpace(resp.Header.Get("scnt")); v != "" {
		f.scnt = v
	}
	if v := strings.TrimSpace(resp.Header.Get("X-Apple-ID-Session-Id")); v != "" {
		f.sessionID = v
	}
	return resp, body, nil
}

// loginJar wraps a cookie jar and records each cookie's attributes, which
// http.CookieJar.Cookies does not return.
type loginJar struct {
	inner http.CookieJar
	mu    sync.Mutex
	seen  []Cookie
}

// SetCookies implements http.CookieJar.
func (j *loginJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.inner.SetCookies(u, cookies)
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		rec := Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   strings.ToLower(strings.TrimPrefix(c.Domain, ".")),
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HttpOnly,
		}
		if rec.Domain == "" {
			rec.Domain = strings.ToLower(u.Hostname())
		} else {
			rec.Domain = "." + rec.Domain
		}
		if rec.Path == "" || !strings.HasPrefix(rec.Path, "/") {
			rec.Path = loginDefaultPath(u.Path)
		}
		switch {
		case c.MaxAge > 0:
			rec.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second).UTC()
		case !c.Expires.IsZero():
			rec.Expires = c.Expires.UTC()
		}
		replaced := false
		for i := range j.seen {
			if j.seen[i].Name == rec.Name && j.seen[i].Domain == rec.Domain && j.seen[i].Path == rec.Path {
				j.seen[i] = rec
				replaced = true
				break
			}
		}
		if !replaced {
			j.seen = append(j.seen, rec)
		}
	}
}

// Cookies implements http.CookieJar.
func (j *loginJar) Cookies(u *url.URL) []*http.Cookie { return j.inner.Cookies(u) }

// collect returns the recorded Apple cookies the jar still holds (deleted or
// expired ones drop out), including two-factor trust cookies (DES*).
func (j *loginJar) collect(now time.Time) []Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Cookie
	for _, c := range j.seen {
		host := strings.TrimPrefix(c.Domain, ".")
		if !loginAppleDomain(host) {
			continue
		}
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			continue
		}
		live := j.inner.Cookies(&url.URL{Scheme: "https", Host: host, Path: c.Path})
		for _, l := range live {
			if l.Name == c.Name && l.Value == c.Value {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// loginAppleDomain reports whether a cookie domain belongs to Apple (or to
// the configured sign-in hosts).
func loginAppleDomain(host string) bool {
	if host == "apple.com" || strings.HasSuffix(host, ".apple.com") {
		return true
	}
	for _, base := range []string{idmsaBase, ascBase} {
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}

// loginDefaultPath is RFC 6265 §5.1.4's default cookie path.
func loginDefaultPath(p string) string {
	if p == "" || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndex(p, "/")
	if i == 0 {
		return "/"
	}
	return p[:i]
}
