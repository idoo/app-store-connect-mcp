package browser

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// windowSize is the interactive window size: a normal window, so it is not
// mistaken for one of the user's own browser windows.
const windowSize = "1100,900"

// start launches the browser and returns a chromedp context bound to one tab.
// cleanup is always non-nil and closes the browser gracefully (cookies are
// flushed to the profile), killing it if it does not exit.
func start(ctx context.Context, o Options, exe string) (context.Context, func(), error) {
	if !o.Headless && canLaunchApp {
		if bundle := appBundle(exe); bundle != "" {
			return startApp(ctx, o, bundle)
		}
	}
	return startExec(ctx, o, exe)
}

// cookieKey says how the profile's cookies are encrypted: mockKeychain and a
// non-empty passwordStore select Chromium's constant key. On macOS a
// vendor-signed Chrome, Brave or Edge uses neither, so cookies are sealed with
// the browser's own "Safe Storage" keychain item. Chrome for Testing keeps the
// mock keychain (it is unsigned, and a keychain item for it would prompt on
// every launch). Linux and Windows keep the basic store: a server has no
// unlocked keyring, and Windows ignores it (cookies use DPAPI).
func cookieKey(goos, exe string) (mockKeychain bool, passwordStore string) {
	if goos == "darwin" && !isCfT(exe) {
		return false, ""
	}
	return true, "basic"
}

// cookieKeyArgs is cookieKey as command-line flags.
func cookieKeyArgs(goos, exe string) []string {
	var args []string
	mock, store := cookieKey(goos, exe)
	if mock {
		args = append(args, "--use-mock-keychain")
	}
	if store != "" {
		args = append(args, "--password-store="+store)
	}
	return args
}

// isCfT reports whether exe (or an .app bundle path) is Chrome for Testing.
// Only the macOS name is distinctive; elsewhere the answer does not matter.
func isCfT(exe string) bool {
	return strings.HasPrefix(filepath.Base(exe), "Google Chrome for Testing")
}

// startExec runs the browser as our child process (headless, non-macOS, or a
// bare executable outside an .app bundle).
func startExec(ctx context.Context, o Options, exe string) (context.Context, func(), error) {
	mock, store := cookieKey(runtime.GOOS, exe)
	var passwordStore any = false // false drops chromedp's default flag
	if store != "" {
		passwordStore = store
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(exe),
		chromedp.UserDataDir(o.ProfileDir),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("use-mock-keychain", mock),
		chromedp.Flag("password-store", passwordStore),
	)
	if !o.Headless {
		opts = append(opts, chromedp.VisibleWindow,
			chromedp.Flag("start-maximized", false),
			chromedp.Flag("window-size", windowSize))
	}
	// The allocator kills the process when ctx is cancelled.
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	bctx, cancelB := chromedp.NewContext(allocCtx)
	cleanup := func() {
		cctx, cancel := context.WithTimeout(bctx, 10*time.Second)
		_ = chromedp.Cancel(cctx)
		cancel()
		cancelB()
		cancelAlloc()
		// Every exit path, including a cancelled caller ctx: no helper
		// process may outlive us holding the profile.
		if !waitProfileExit(o.ProfileDir, 2*time.Second) {
			killProfile(o.ProfileDir)
		}
	}
	// First call starts the browser; it must not carry the overall timeout.
	if err := chromedp.Do(bctx); err != nil {
		return nil, cleanup, fmt.Errorf("browser: start %s: %w", exe, err)
	}
	return bctx, cleanup, nil
}

// startApp launches the .app through LaunchServices so its window comes to
// the front, then attaches over the DevTools websocket.
func startApp(ctx context.Context, o Options, bundle string) (context.Context, func(), error) {
	ws, err := launchApp(ctx, bundle, o.ProfileDir, "about:blank")
	if err != nil {
		return nil, func() {}, err
	}
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, ws, remote.NoModifyURL)
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	cancelTab := func() {}
	cleanup := func() {
		cancelTab()
		cancelRoot()
		cancelAlloc()
		closeApp(ws, o.ProfileDir)
	}
	infos, err := chromedp.CallBrowser(rootCtx, target.GetTargets, target.GetTargetsParams{})
	if err != nil {
		return nil, cleanup, fmt.Errorf("browser: attach to %s: %w", bundle, err)
	}
	var tabOpts []chromedp.ContextOption
	for _, t := range infos.TargetInfos {
		if t.Type == "page" {
			tabOpts = append(tabOpts, chromedp.WithTargetID(t.TargetID))
			break
		}
	}
	bctx, cancelB := chromedp.NewContext(rootCtx, tabOpts...)
	cancelTab = cancelB
	if err := chromedp.Do(bctx); err != nil {
		return nil, cleanup, fmt.Errorf("browser: attach to %s tab: %w", bundle, err)
	}
	_, _ = chromedp.Call(bctx, page.BringToFront, cdp.Empty{})
	return bctx, cleanup, nil
}

// closeApp closes a browser we did not start as a child: Browser.close over a
// fresh connection (the caller's may be cancelled), then wait for exit, then
// kill the profile's processes.
func closeApp(ws, profile string) {
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := remote.NewAllocator(cctx, ws, remote.NoModifyURL)
	bctx, cancelB := chromedp.NewContext(allocCtx)
	_, _ = chromedp.CallBrowser(bctx, cdpbrowser.Close, cdp.Empty{})
	cancelB()
	cancelAlloc()
	if !waitProfileExit(profile, 5*time.Second) {
		killProfile(profile)
	}
}
