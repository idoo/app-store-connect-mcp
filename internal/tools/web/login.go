package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "web_login", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "Sign in to the App Store Connect web session",
		Description: "Open a browser window on the user's machine so they sign in to App Store Connect on Apple's own page " +
			"(password, 2FA, passkey). You never see credentials. Reuses a valid session without a window. " +
			"Call when a tool fails with web_session_required.",
	}, webLogin))
}

type webLoginIn struct{}

type webLoginOut struct {
	Account tool.WebAccount `json:"account"`
	Next    string          `json:"next"`
}

// loginCooldown is how long web_login refuses new calls after a failed or
// cancelled sign-in, so an agent cannot open window after window.
const loginCooldown = 60 * time.Second

// loginGate allows one sign-in at a time per process and enforces
// [loginCooldown] after a failure.
type loginGate struct {
	mu       sync.Mutex
	busy     bool
	failedAt time.Time
}

// gate is the process-wide sign-in gate: every MCP session of this server
// shares one browser profile per credentials profile and one human.
var gate loginGate

var (
	errLoginUnavailable = &tool.Error{
		Code:    tool.ErrUnavailable.Code,
		Message: "signing in from a tool is not available in this server",
		Hint:    "ask the user to run `app-store-connect-mcp web login` in a terminal on the machine running the server, then call again",
	}
	errLoginBusy = &tool.Error{
		Code:    "login_in_progress",
		Message: "a sign-in window is already open",
		Hint:    "ask the user to finish signing in in the open browser window; do not call web_login again until this call returns",
	}
	errLoginCooldown = &tool.Error{
		Code:    "login_cooldown",
		Message: "the last sign-in failed or was cancelled",
		Hint:    "ask the user whether they want to sign in now before calling web_login again",
	}
	errLoginClosed = &tool.Error{
		Code:    "login_cancelled",
		Message: "the browser window was closed before sign-in finished",
		Hint:    "ask the user whether they want to sign in; call web_login again only after they agree",
	}
	errLoginTimeout = &tool.Error{
		Code:    "login_timeout",
		Message: "sign-in timed out: nobody finished signing in in the browser window",
		Hint:    "ask the user to be ready at the computer, then call web_login again",
	}
	errLoginNoBrowser = &tool.Error{
		Code:    "no_browser",
		Message: "no usable Chromium-based browser for the sign-in window",
		Hint:    "ask the user to install Chrome, Brave or Edge, set ASC_MCP_BROWSER, or run `app-store-connect-mcp web login --password` / `web import` in a terminal",
	}
	errLoginFailed = &tool.Error{
		Code:    "login_failed",
		Message: "sign-in failed",
		Hint:    "show the message to the user; they can also run `app-store-connect-mcp web login` in a terminal",
	}
)

// acquire takes the gate at now or says why it cannot.
func (g *loginGate) acquire(now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy {
		return errLoginBusy
	}
	if !g.failedAt.IsZero() {
		if wait := g.failedAt.Add(loginCooldown).Sub(now); wait > 0 {
			return fmt.Errorf("%w; web_login is paused for %d more seconds", errLoginCooldown, int(math.Ceil(wait.Seconds())))
		}
	}
	g.busy = true
	return nil
}

// release frees the gate; a failure at now starts the cooldown, a success
// clears it.
func (g *loginGate) release(now time.Time, failed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy = false
	if failed {
		g.failedAt = now
	} else {
		g.failedAt = time.Time{}
	}
}

func webLogin(ctx context.Context, env *tool.Env, _ webLoginIn) (*tool.Result, error) {
	if env.WebLogin == nil {
		return nil, fmt.Errorf("%w: %w", errLoginUnavailable, tool.ErrUnavailable)
	}
	if err := gate.acquire(env.Clock.Now()); err != nil {
		return nil, err
	}
	var (
		mu    sync.Mutex
		lines float64
	)
	acct, err := env.WebLogin(ctx, func(line string) {
		mu.Lock()
		lines++
		n := lines
		mu.Unlock()
		env.Progress(n, 0, line)
	})
	gate.release(env.Clock.Now(), err != nil)
	if err != nil {
		return nil, explainLoginError(err)
	}
	next := "signed in; call the tool that needed the web session again"
	if acct.Reused {
		next = "the existing session is still valid; call the tool that needed the web session again"
	}
	return tool.OK(webLoginOut{Account: acct, Next: next}), nil
}

// explainLoginError puts a typed error with a hint in front of err, keeping
// err in the chain.
func explainLoginError(err error) error {
	var te *tool.Error
	switch {
	case errors.Is(err, browser.ErrClosed):
		return fmt.Errorf("%w: %w", errLoginClosed, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", errLoginTimeout, err)
	case errors.Is(err, browser.ErrNoBrowser):
		return fmt.Errorf("%w: %w", errLoginNoBrowser, err)
	case errors.Is(err, context.Canceled), errors.As(err, &te):
		return err
	}
	return fmt.Errorf("%w: %w", errLoginFailed, err)
}
