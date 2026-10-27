package websession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
)

const (
	// browserStartURL is where the browser sign-in begins.
	browserStartURL = ASCOrigin + "/login"
	// verifyEvery bounds /olympus/v1/session checks while the cookies that
	// identify the sign-in (myacinfo, dqsid) stay the same.
	verifyEvery = 10 * time.Second
	// rememberAttr marks a checkbox the script already ticked once.
	rememberAttr = "data-ascmcp-remember"
)

// browserCookieURLs makes Network.getCookies return the host cookies of App
// Store Connect (including path-scoped /iris and /olympus ones), idmsa and
// the shared .apple.com cookies.
var browserCookieURLs = []string{
	ASCOrigin + "/",
	ASCOrigin + "/iris/",
	ASCOrigin + "/olympus/",
	"https://idmsa.apple.com/",
	"https://www.apple.com/",
}

// rememberMeScript ticks Apple's "Keep me signed in" checkbox once per
// element; a human who unticks it afterwards is respected.
const rememberMeScript = `(function(){
var seen = new Set();
var boxes = [];
var add = function(el){ if (el && el.type === "checkbox" && !seen.has(el)) { seen.add(el); boxes.push(el); } };
add(document.querySelector('input#remember-me[type=checkbox]'));
document.querySelectorAll('input[type=checkbox]').forEach(function(el){
  if (/remember/i.test(el.id || "") || /remember/i.test(el.name || "")) { add(el); return; }
  var lab = el.closest("label") || (el.id ? document.querySelector('label[for="' + CSS.escape(el.id) + '"]') : null);
  if (lab && /remember|keep me signed in/i.test(lab.textContent || "")) { add(el); }
});
boxes.forEach(function(el){
  if (el.hasAttribute("` + rememberAttr + `")) { return; }
  el.setAttribute("` + rememberAttr + `", "1");
  if (!el.checked && !el.disabled) { el.click(); }
});
})();`

// browserLogin is browser.Login; tests replace it.
var browserLogin = browser.Login

// BrowserProfileDir is where the dedicated browser profile for a credentials
// profile lives: <stateDir>/web/browser/<safe profile name>.
func BrowserProfileDir(stateDir, profile string) string {
	return filepath.Join(stateDir, "web", "browser", safeProfileName(profile))
}

// BrowserLoginOptions configures [BrowserLogin].
type BrowserLoginOptions struct {
	ProfileDir  string        // dedicated browser user-data-dir, see BrowserProfileDir
	BrowserPath string        // "" = auto-detect (browser.EnvBrowser, default browser, …)
	Headless    bool          // silent renewal: fail with browser.ErrInteractionRequired instead of asking
	Timeout     time.Duration // overall; 0 = the browser package default
	Log         io.Writer     // progress lines for the human; never secrets; nil = discard
	HTTP        *http.Client  // for the /olympus/v1/session verification; nil = 30 s client
	Now         func() time.Time
}

// BrowserLogin opens App Store Connect in the dedicated browser profile, waits until the
// cookies yield a valid /olympus/v1/session, and returns the session (Source "browser"),
// AppleID and team filled from the olympus answer. Only unexpired *.apple.com cookies
// are kept. browser.ErrNoBrowser, browser.ErrClosed and
// browser.ErrInteractionRequired stay matchable with errors.Is.
func BrowserLogin(ctx context.Context, o BrowserLoginOptions) (*Session, error) {
	if o.ProfileDir == "" {
		return nil, errors.New("websession: browser login: profile directory is required")
	}
	logw := o.Log
	if logw == nil {
		logw = io.Discard
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &browserCheck{hc: o.HTTP, now: o.Now, log: logw}
	if !o.Headless {
		_, _ = fmt.Fprintln(logw, "Opening a browser window… sign in to App Store Connect in the window.")
	}
	res, err := browserLogin(ctx, browser.Options{
		ProfileDir:  o.ProfileDir,
		BrowserPath: o.BrowserPath,
		Headless:    o.Headless,
		Timeout:     o.Timeout,
		Log:         logw,
	}, browser.Request{
		StartURL:    browserStartURL,
		CookieURLs:  browserCookieURLs,
		Check:       c.check,
		NeedsHuman:  needsHuman,
		FrameScript: rememberMeScript,
	})
	if err != nil {
		return nil, fmt.Errorf("websession: browser login: %w", err)
	}
	if c.info == nil {
		return nil, errors.New("websession: browser login: the browser finished without a verified session")
	}
	sess := &Session{
		AppleID:      c.info.AppleID,
		ProviderID:   c.info.Provider.ID,
		ProviderName: c.info.Provider.Name,
		Cookies:      appleCookies(res.Cookies, o.Now()),
		CreatedAt:    o.Now(),
		Source:       "browser",
	}
	_, _ = fmt.Fprintf(logw, "Signed in as %s (team %s).\n", sess.AppleID, teamLabel(sess))
	return sess, nil
}

func teamLabel(s *Session) string {
	if s.ProviderName != "" {
		return s.ProviderName
	}
	return fmt.Sprint(s.ProviderID)
}

// browserCheck is the browser.Request Check: it verifies the captured
// cookies against olympus, at most every verifyEvery unless myacinfo or
// dqsid changed. browser.Login calls it from one goroutine.
type browserCheck struct {
	hc  *http.Client
	now func() time.Time
	log io.Writer

	lastPrint  string
	lastVerify time.Time
	lastErr    string
	info       *Info
}

func (c *browserCheck) check(ctx context.Context, _ string, cookies []browser.Cookie) (bool, error) {
	now := c.now()
	sess := &Session{Cookies: appleCookies(cookies, now)}
	fp := identityPrint(sess)
	if fp == "" {
		return false, nil
	}
	if fp == c.lastPrint && now.Sub(c.lastVerify) < verifyEvery {
		return false, nil
	}
	c.lastPrint, c.lastVerify = fp, now
	info, err := Validate(ctx, c.hc, sess, now)
	switch {
	case err == nil:
		c.info = info
		return true, nil
	case errors.Is(err, ErrExpired) || ctx.Err() != nil:
		return false, nil
	default:
		// Validate errors carry status codes or transport errors, never cookie values.
		if msg := err.Error(); msg != c.lastErr {
			c.lastErr = msg
			_, _ = fmt.Fprintf(c.log, "Could not verify the App Store Connect session yet (%s); still waiting.\n", msg)
		}
		return false, nil
	}
}

// identityPrint hashes the myacinfo and dqsid values; "" without myacinfo.
func identityPrint(s *Session) string {
	var myac, dq []string
	for _, ck := range s.Cookies {
		switch ck.Name {
		case "myacinfo":
			if ck.Value != "" {
				myac = append(myac, ck.Value)
			}
		case "dqsid":
			dq = append(dq, ck.Value)
		}
	}
	if len(myac) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(myac, "\x00") + "\x01" + strings.Join(dq, "\x00")))
	return hex.EncodeToString(sum[:])
}

// appleCookies converts browser cookies, keeping unexpired *.apple.com ones.
func appleCookies(in []browser.Cookie, now time.Time) []Cookie {
	out := make([]Cookie, 0, len(in))
	for _, c := range in {
		if c.Name == "" || !isAppleDomain(c.Domain) {
			continue
		}
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			continue
		}
		out = dedupe(out, Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly,
		})
	}
	return out
}

// needsHuman reports Apple's interactive sign-in pages.
func needsHuman(pageURL string) bool {
	u, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "idmsa.apple.com":
		return true
	case "appstoreconnect.apple.com":
		return u.Path == "/login" || strings.HasPrefix(u.Path, "/login/")
	}
	return false
}
