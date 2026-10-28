package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "web_login", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "Sign in to the App Store Connect web session",
		Description: "Open a browser window on the user's machine to sign in to App Store Connect on Apple's page " +
			"(password, 2FA, passkey); you never see credentials. Reuses a valid session. Answers pending within seconds " +
			"while the user signs in: call again. Use on web_session_required.",
	}, webLogin))
}

type webLoginIn struct{}

type webLoginOut struct {
	Status  string          `json:"status"`
	Account tool.WebAccount `json:"account,omitzero"`
	Log     []string        `json:"log,omitempty"`
	Next    string          `json:"next"`
}

// loginWait is how long one web_login call waits for the sign-in before it
// answers pending: MCP clients time requests out after ~30 s, a human needs
// minutes. A still-valid stored session is reused well within it.
var loginWait = 5 * time.Second

// maxLoginLog bounds the progress lines a pending answer repeats.
const maxLoginLog = 5

// loginCooldown is how long web_login refuses new calls after a failed or
// cancelled sign-in, so an agent cannot open window after window.
const loginCooldown = 60 * time.Second

// loginRun is one sign-in running in the background, or finished and not
// yet reported to a caller.
type loginRun struct {
	done chan struct{}
	acct tool.WebAccount
	err  error

	mu       sync.Mutex
	lines    []string
	progress func(string) // the waiting caller's progress, nil when none waits
}

// log records a progress line and forwards it to the waiting caller.
func (r *loginRun) log(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if r.progress != nil {
		r.progress(line)
	}
}

// attach forwards progress lines to fn until the returned func is called.
func (r *loginRun) attach(fn func(string)) (detach func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = fn
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.progress = nil
	}
}

// recentLines is the tail of the progress log.
func (r *loginRun) recentLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines[max(0, len(r.lines)-maxLoginLog):])
}

// loginGate allows one sign-in at a time per process and enforces
// [loginCooldown] after a failure.
type loginGate struct {
	mu       sync.Mutex
	run      *loginRun
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

// start returns the sign-in in progress (or finished and unreported), else
// starts one at now in the background, or says why it cannot. The sign-in
// outlives the call that started it: the human needs minutes.
func (g *loginGate) start(ctx context.Context, env *tool.Env, now time.Time) (*loginRun, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.run != nil {
		return g.run, nil
	}
	if !g.failedAt.IsZero() {
		if wait := g.failedAt.Add(loginCooldown).Sub(now); wait > 0 {
			return nil, fmt.Errorf("%w; web_login is paused for %d more seconds", errLoginCooldown, int(math.Ceil(wait.Seconds())))
		}
	}
	run := &loginRun{done: make(chan struct{})}
	g.run = run
	login, bg := env.WebLogin, context.WithoutCancel(ctx)
	go func() {
		defer close(run.done)
		run.acct, run.err = login(bg, run.log)
	}()
	return run, nil
}

// finish reports run's result once: it frees the gate; a failure at now
// starts the cooldown, a success clears it.
func (g *loginGate) finish(run *loginRun, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.run != run {
		return
	}
	g.run = nil
	if run.err != nil {
		g.failedAt = now
	} else {
		g.failedAt = time.Time{}
	}
}

func webLogin(ctx context.Context, env *tool.Env, _ webLoginIn) (*tool.Result, error) {
	if env.WebLogin == nil {
		return nil, fmt.Errorf("%w: %w", errLoginUnavailable, tool.ErrUnavailable)
	}
	run, err := gate.start(ctx, env, env.Clock.Now())
	if err != nil {
		return nil, err
	}
	var lines float64
	detach := run.attach(func(line string) { // called under run.mu: serialised
		lines++
		env.Progress(lines, 0, line)
	})
	timer := time.NewTimer(loginWait)
	defer timer.Stop()
	select {
	case <-run.done:
		detach()
	case <-timer.C:
		detach()
		return tool.OK(webLoginOut{
			Status: "pending",
			Log:    run.recentLines(),
			Next: "a sign-in window is open on the user's machine: ask the user to sign in there, then call web_login " +
				"again (it never opens a second window) until status is ok",
		}), nil
	case <-ctx.Done():
		detach()
		return nil, ctx.Err()
	}
	gate.finish(run, env.Clock.Now())
	if run.err != nil {
		return nil, explainLoginError(run.err)
	}
	next := "signed in; call the tool that needed the web session again"
	if run.acct.Reused {
		next = "the existing session is still valid; call the tool that needed the web session again"
	}
	return tool.OK(webLoginOut{Status: "ok", Account: run.acct, Next: next}), nil
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
