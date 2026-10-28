package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// ErrInteractionRequired means a headless run reached a page that needs the
// human (sign-in form, 2FA) or never finished signing in.
var ErrInteractionRequired = errors.New("the human needs to sign in")

// ErrClosed means the human closed the window (or the browser went away)
// before sign-in finished.
var ErrClosed = errors.New("browser closed before sign-in finished")

// signInHint is printed when the interactive window opens; %s is the browser.
const signInHint = "Sign in in the %s window that just opened (a separate app-store-connect-mcp profile; your own browser profile is not touched). The window closes by itself when sign-in is done."

const pollEvery = time.Second

// Options configures the browser launch.
type Options struct {
	ProfileDir  string        // dedicated user-data-dir; created 0700
	BrowserPath string        // "" = auto-detect (EnvBrowser, default browser, other Chromium, downloaded CfT)
	Headless    bool          // silent renewal
	Timeout     time.Duration // overall; default 10 min interactive, 60 s headless
	Log         io.Writer     // human-readable progress lines; never secrets; nil = discard
	BinDir      string        // Chrome for Testing dir; "" = <state dir>/browser-bin
}

// Cookie is a browser cookie.
type Cookie struct {
	Name, Value, Domain, Path string
	Expires                   time.Time // zero = session cookie
	HTTPOnly, Secure          bool
}

// Request says where Login starts and when it is done.
type Request struct {
	StartURL   string   // opened first, e.g. https://appstoreconnect.apple.com/login
	CookieURLs []string // Network.getCookies urls, e.g. https://appstoreconnect.apple.com/, https://idmsa.apple.com/
	// Check runs on every poll (~1 s) with the cookies for CookieURLs and the
	// top-level page URL; done=true ends Login successfully with those cookies.
	// A non-nil error aborts Login (wrapped). Check must be cheap when nothing changed.
	Check func(ctx context.Context, pageURL string, cookies []Cookie) (done bool, err error)
	// NeedsHuman reports that pageURL is an interactive sign-in page; in a
	// headless run Login then returns ErrInteractionRequired at once. Optional.
	NeedsHuman func(pageURL string) bool
	// FrameScript is JavaScript evaluated best-effort in every frame,
	// including cross-origin (OOPIF) iframes, on every poll, e.g. to tick a
	// "Keep me signed in" checkbox. It runs in an isolated world per document
	// and must be idempotent. Errors are ignored. Optional.
	FrameScript string
}

// Result is what Login captured.
type Result struct {
	Cookies []Cookie // the cookies Check accepted
	Browser string   // display name, e.g. "Brave Browser"
}

// Login launches the browser with the dedicated profile, opens r.StartURL and
// polls until r.Check accepts the cookies. The browser is always closed
// before Login returns.
func Login(ctx context.Context, o Options, r Request) (Result, error) {
	if r.StartURL == "" {
		return Result{}, errors.New("browser: StartURL is required")
	}
	if r.Check == nil {
		return Result{}, errors.New("browser: Check is required")
	}
	if o.ProfileDir == "" {
		return Result{}, errors.New("browser: ProfileDir is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
		if o.Headless {
			o.Timeout = 60 * time.Second
		}
	}
	logw := o.Log
	if logw == nil {
		logw = io.Discard
	}
	if !o.Headless && !HasDisplay() {
		return Result{}, errors.New("browser: no display (DISPLAY and WAYLAND_DISPLAY are unset); sign in on a desktop session, or use `app-store-connect-mcp web import`")
	}
	if o.BinDir == "" {
		o.BinDir = defaultBinDir()
	}
	exe, name, note, err := resolveBrowser(ctx, o.BrowserPath, o.BinDir, logw)
	if err != nil {
		return Result{}, err
	}
	if note != "" {
		fmt.Fprintln(logw, note)
	}
	if err := os.MkdirAll(o.ProfileDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("browser: create profile dir: %w", err)
	}
	_ = os.Chmod(o.ProfileDir, 0o700) //nolint:gosec // G302: a directory needs the x bit; 0700 is owner-only
	if err := writePreferences(o.ProfileDir); err != nil {
		return Result{}, err
	}
	excludeFromBackup(o.ProfileDir)
	// A headless renew orphaned by a killed server would hold the profile lock.
	killOrphans(o.ProfileDir)

	bctx, cleanup, err := start(ctx, o, exe)
	defer cleanup()
	if err != nil {
		return Result{}, err
	}
	if o.Headless {
		v, err := chromedp.CallBrowser(bctx, cdpbrowser.GetVersion, cdp.Empty{})
		if err != nil {
			return Result{}, fmt.Errorf("browser: read version: %w", err)
		}
		if _, err := chromedp.Call(bctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{UserAgent: headfulUA(v.UserAgent)}); err != nil {
			return Result{}, fmt.Errorf("browser: set user agent: %w", err)
		}
	}

	wctx, cancelW := context.WithTimeout(bctx, o.Timeout)
	defer cancelW()
	s := &session{ctx: wctx, parent: ctx, headless: o.Headless, timeout: o.Timeout, name: name}
	if t := chromedp.FromContext(bctx).Target; t != nil {
		s.targetID = t.TargetID
	}
	if _, err := chromedp.Call(s.ctx, page.Navigate, page.NavigateParams{URL: r.StartURL}); err != nil {
		return Result{}, fmt.Errorf("browser: open sign-in page: %w", s.wrap(err))
	}
	if !o.Headless {
		fmt.Fprintf(logw, signInHint+"\n", name)
	}
	cs, err := s.poll(r)
	if err != nil {
		return Result{}, err
	}
	return Result{Cookies: cs, Browser: name}, nil
}

// decide is one poll's verdict: Check first (a signed-in profile may still
// show a sign-in URL for a moment), then, headless only, whether the page
// needs a person.
func decide(ctx context.Context, headless bool, pageURL string, cookies []Cookie, r Request) (bool, error) {
	done, err := r.Check(ctx, pageURL, cookies)
	if err != nil {
		return false, fmt.Errorf("browser: sign-in check: %w", err)
	}
	if done {
		return true, nil
	}
	if headless && r.NeedsHuman != nil && r.NeedsHuman(pageURL) {
		return false, fmt.Errorf("%w (%s asks for a sign-in)", ErrInteractionRequired, pageLabel(pageURL))
	}
	return false, nil
}

type session struct {
	ctx      context.Context // overall timeout, child of the browser context
	parent   context.Context // caller's context
	headless bool
	timeout  time.Duration
	targetID target.ID
	name     string // browser display name
}

// poll runs the sign-in loop until Check accepts the cookies.
func (s *session) poll(r Request) ([]Cookie, error) {
	frames := newFrameRunner(s.ctx, r.FrameScript)
	defer frames.close()
	for {
		if err := s.sleep(); err != nil {
			return nil, err
		}
		u, ok, err := s.location()
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		frames.run()
		cs, err := s.cookies(r.CookieURLs)
		if err != nil {
			return nil, err
		}
		done, err := decide(s.ctx, s.headless, u, cs, r)
		if err != nil {
			if s.ctx.Err() != nil || s.parent.Err() != nil {
				return nil, s.wrap(err)
			}
			return nil, err
		}
		if done {
			return cs, nil
		}
	}
}

func (s *session) sleep() error {
	t := time.NewTimer(pollEvery)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-s.ctx.Done():
		return s.wrap(s.ctx.Err())
	}
}

// location reads the top-level page URL; ok=false on a transient error.
func (s *session) location() (string, bool, error) {
	res, err := chromedp.CallBrowser(s.ctx, target.GetTargetInfo, target.GetTargetInfoParams{TargetID: s.targetID})
	if err == nil && res.TargetInfo != nil {
		return res.TargetInfo.URL, true, nil
	}
	if s.ctx.Err() != nil || s.parent.Err() != nil {
		return "", false, s.wrap(err)
	}
	if s.targetGone() {
		return "", false, ErrClosed
	}
	return "", false, nil
}

// cookies reads the cookies for urls from the page's session.
func (s *session) cookies(urls []string) ([]Cookie, error) {
	res, err := chromedp.Call(s.ctx, network.GetCookies, network.GetCookiesParams{URLs: urls})
	if err != nil {
		if s.ctx.Err() == nil && s.parent.Err() == nil && s.targetGone() {
			return nil, ErrClosed
		}
		return nil, fmt.Errorf("browser: read cookies: %w", s.wrap(err))
	}
	out := make([]Cookie, 0, len(res.Cookies))
	for _, c := range res.Cookies {
		if c == nil {
			continue
		}
		out = append(out, Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: cdpExpires(c.Expires), HTTPOnly: c.HTTPOnly, Secure: c.Secure,
		})
	}
	return out, nil
}

// targetGone reports whether our tab (or the whole browser) has gone away.
func (s *session) targetGone() bool {
	if s.ctx.Err() != nil {
		return false
	}
	res, err := chromedp.CallBrowser(s.ctx, target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return s.ctx.Err() == nil
	}
	for _, t := range res.TargetInfos {
		if t.TargetID == s.targetID {
			return false
		}
	}
	return s.targetID != ""
}

// wrap maps context/browser errors to the package's errors.
func (s *session) wrap(err error) error {
	if err == nil {
		return nil
	}
	if s.parent.Err() != nil {
		return s.parent.Err()
	}
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		if s.headless {
			return fmt.Errorf("%w: sign-in not finished within %s: %w", ErrInteractionRequired, s.timeout, context.DeadlineExceeded)
		}
		return fmt.Errorf("browser: timed out waiting for sign-in after %s: %w", s.timeout, context.DeadlineExceeded)
	}
	if s.ctx.Err() != nil {
		return ErrClosed
	}
	return err
}
