package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/notify"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// Test hooks for keepalive and the sign-in window.
var (
	// webNotify tells the human something needs them.
	webNotify = notify.Desktop
	// keepaliveHTTP talks to Apple; nil uses a 30 s client without redirects.
	keepaliveHTTP *http.Client
)

// keepaliveTitle is the desktop notification title.
const keepaliveTitle = "App Store Connect"

// ErrKeepaliveSignIn means the session is dead and could not be renewed
// silently: the human must sign in.
var ErrKeepaliveSignIn = errors.New("sign-in needed: run `app-store-connect-mcp web login`")

// ErrKeepaliveWindowOpen means the session is dead and a sign-in window
// already runs with the dedicated profile: nothing renews or notifies.
var ErrKeepaliveWindowOpen = errors.New("sign-in window open, skipping")

// WebKeepalive uses profile's stored web session once (GET
// /olympus/v1/session) so Apple keeps it alive, saving the cookies Apple
// rolls. A rejected session is renewed silently from the dedicated browser
// profile when an earlier browser sign-in exists. When that fails too, it
// notifies the human once per dead session and returns ErrKeepaliveSignIn.
// It returns the signed-in Apple ID when live.
func WebKeepalive(ctx context.Context, stateDir, profile string) (string, error) {
	src := newWebSource(profile, stateDir)
	marker := keepaliveMarker(stateDir, profile)
	apple, err := keepaliveCheck(ctx, src)
	if err == nil {
		_ = os.Remove(marker)
		return apple, nil
	}
	if errors.Is(err, browser.ErrProfileInUse) {
		return "", fmt.Errorf("web session of profile %s: %w", profile, ErrKeepaliveWindowOpen)
	}
	if !errors.Is(err, tool.ErrWebSessionRequired) && !errors.Is(err, websession.ErrExpired) {
		return "", err
	}
	fp := "none"
	if sess, lerr := websession.DefaultStore(stateDir).Load(profile); lerr == nil {
		fp = websession.Fingerprint(sess)
	}
	if old, rerr := os.ReadFile(marker); rerr != nil || string(old) != fp { //nolint:gosec // G304: our state dir path
		webNotify(keepaliveTitle, "Sign-in needed: run `app-store-connect-mcp web login`")
		if merr := os.MkdirAll(filepath.Dir(marker), 0o700); merr == nil {
			_ = os.WriteFile(marker, []byte(fp), 0o600)
		}
	}
	return "", fmt.Errorf("web session of profile %s: %w", profile, ErrKeepaliveSignIn)
}

// keepaliveMarker remembers which dead session the human was told about.
func keepaliveMarker(stateDir, profile string) string {
	return filepath.Join(stateDir, "web", "keepalive", filepath.Base(websession.BrowserProfileDir(stateDir, profile))+".notified")
}

// keepaliveCheck asks Apple for the session through src: Authorize supplies
// the cookies (renewing when none are usable), Observe stores rolled ones.
// A rejection marks the session expired and tries once more, which renews.
func keepaliveCheck(ctx context.Context, src *websession.Source) (string, error) {
	hc := keepaliveHTTP
	if hc == nil {
		hc = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, websession.OlympusSessionURL, nil)
		if err != nil {
			return "", err
		}
		if err := src.Authorize(ctx, req); err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "olympus-ui")
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("keepalive: %w", err)
		}
		src.Observe(resp)
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("keepalive: %w", err)
		}
		apple, live, err := websession.ParseSession(resp.StatusCode, body)
		if err != nil {
			return "", fmt.Errorf("keepalive: %w", err)
		}
		if live {
			return apple, nil
		}
		if attempt > 0 {
			return "", fmt.Errorf("session rejected after renewal: %w", tool.ErrWebSessionRequired)
		}
		src.Expired()
	}
}
