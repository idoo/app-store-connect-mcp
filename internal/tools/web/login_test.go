package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// loginKit returns a Kit with a fresh process-wide sign-in gate.
func loginKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	gate = loginGate{}
	t.Cleanup(func() { gate = loginGate{} })
	return tooltest.New(t)
}

type progressLine struct {
	done, total float64
	msg         string
}

func TestWebLoginSuccessForwardsProgress(t *testing.T) {
	k := loginKit(t)
	var (
		mu    sync.Mutex
		lines []progressLine
	)
	k.Env.Progress = func(done, total float64, msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, progressLine{done, total, msg})
	}
	k.Env.WebLogin = func(_ context.Context, log func(string)) (tool.WebAccount, error) {
		log("opening Brave Browser")
		log("signed in")
		return tool.WebAccount{AppleID: "dev@example.com", TeamID: 42, TeamName: "Jvcr"}, nil
	}
	out := tooltest.Decode[webLoginOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "web_login"), k.Env, `{}`)))
	if out.Account != (tool.WebAccount{AppleID: "dev@example.com", TeamID: 42, TeamName: "Jvcr"}) || !strings.Contains(out.Next, "again") {
		t.Fatalf("out = %+v", out)
	}
	want := []progressLine{{1, 0, "opening Brave Browser"}, {2, 0, "signed in"}}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Errorf("progress = %v, want %v", lines, want)
	}

	// A reused session says so and does not start a cooldown.
	k.Env.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) {
		return tool.WebAccount{AppleID: "dev@example.com", Reused: true}, nil
	}
	out = tooltest.Decode[webLoginOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "web_login"), k.Env, `{}`)))
	if !out.Account.Reused || !strings.Contains(out.Next, "still valid") {
		t.Errorf("reused = %+v", out)
	}
}

func TestWebLoginUnavailable(t *testing.T) {
	k := loginKit(t)
	b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "web_login"), k.Env, `{}`), tool.ErrUnavailable.Code)
	if !strings.Contains(b.Hint, "app-store-connect-mcp web login") {
		t.Errorf("hint = %q", b.Hint)
	}
}

func TestWebLoginOneAtATime(t *testing.T) {
	k := loginKit(t)
	started, finish := make(chan struct{}), make(chan struct{})
	k.Env.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) {
		close(started)
		<-finish
		return tool.WebAccount{AppleID: "dev@example.com"}, nil
	}
	first := make(chan *tool.Result, 1)
	go func() { first <- mustTool(t, "web_login").Call(context.Background(), k.Env, []byte(`{}`)) }()
	<-started

	second := *k.Env
	second.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) {
		t.Error("a second window was opened")
		return tool.WebAccount{}, nil
	}
	b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "web_login"), &second, `{}`), errLoginBusy.Code)
	if !strings.Contains(b.Message, "already open") {
		t.Errorf("busy = %+v", b)
	}
	close(finish)
	tooltest.MustOK(t, <-first)
	third := *k.Env // the gate is free again once the first call returns
	third.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) {
		return tool.WebAccount{AppleID: "dev@example.com", Reused: true}, nil
	}
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "web_login"), &third, `{}`))
}

func TestWebLoginCooldownAfterFailure(t *testing.T) {
	k := loginKit(t)
	calls := 0
	k.Env.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) {
		calls++
		if calls == 1 {
			return tool.WebAccount{}, errors.New("olympus said no")
		}
		return tool.WebAccount{AppleID: "dev@example.com"}, nil
	}
	call := func() *tool.Result { return tooltest.Call(t, mustTool(t, "web_login"), k.Env, `{}`) }
	b := tooltest.MustFail(t, call(), errLoginFailed.Code)
	if !strings.Contains(b.Message, "olympus said no") {
		t.Errorf("failed = %+v", b)
	}
	start := k.Clock.Now()
	b = tooltest.MustFail(t, call(), errLoginCooldown.Code)
	if !strings.Contains(b.Message, "60 more seconds") {
		t.Errorf("cooldown = %+v", b)
	}
	k.Clock.Set(start.Add(59 * time.Second))
	b = tooltest.MustFail(t, call(), errLoginCooldown.Code)
	if !strings.Contains(b.Message, "1 more seconds") {
		t.Errorf("cooldown at 59 s = %+v", b)
	}
	if calls != 1 {
		t.Fatalf("WebLogin ran %d times during the cooldown", calls)
	}
	k.Clock.Set(start.Add(loginCooldown))
	tooltest.MustOK(t, call())
	if calls != 2 {
		t.Errorf("calls = %d", calls)
	}
}

func TestWebLoginErrorHints(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("sign in: %w", browser.ErrClosed), errLoginClosed.Code},
		{fmt.Errorf("sign in: %w", context.DeadlineExceeded), errLoginTimeout.Code},
		{fmt.Errorf("sign in: %w", browser.ErrNoBrowser), errLoginNoBrowser.Code},
		{fmt.Errorf("check: %w", tool.ErrWebSessionRequired), tool.ErrWebSessionRequired.Code},
		{errors.New("boom"), errLoginFailed.Code},
	} {
		k := loginKit(t)
		k.Env.WebLogin = func(context.Context, func(string)) (tool.WebAccount, error) { return tool.WebAccount{}, c.err }
		b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "web_login"), k.Env, `{}`), c.code)
		if b.Hint == "" {
			t.Errorf("%v: no hint", c.err)
		}
	}
}

func TestWebLoginMeta(t *testing.T) {
	tl := mustTool(t, "web_login")
	m := tl.Meta()
	if m.Toolset != tool.Web || m.Class != tool.Read || !m.Idempotent || len(m.Description) > tool.MaxDescription {
		t.Errorf("meta = %+v (description %d chars)", m, len(m.Description))
	}
	if !tl.ListedIn(tool.ProfileInfo{Name: "ro", KeyKind: tool.KeyTeam}) {
		t.Error("web_login is withheld in read-only mode")
	}
}
