package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/state"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

const webUsage = `usage:
  app-store-connect-mcp web login [--profile NAME] [--team ID] [--force] [--timeout 10m] [--browser PATH]
                                    open a browser window on Apple's own sign-in page (password, 2FA and
                                    passkeys stay in the browser); a dedicated browser profile, never yours
  app-store-connect-mcp web login --password [--profile NAME] [--apple-id EMAIL] [--team ID] [--force] [--password-stdin]
                                    sign in without a browser (headless machines; Apple may refuse it)
  app-store-connect-mcp web import [--profile NAME] [--team ID] [FILE|-]
                                    import cookies: browser "Copy as cURL", a Cookie header,
                                    FASTLANE_SESSION or a Cookie-Editor JSON export (stdin by default)
  app-store-connect-mcp web status [--profile NAME]   show the stored session and check it with Apple (exit 1 unless active)
  app-store-connect-mcp web team [--profile NAME] [ID] list teams, or switch the session to team ID
  app-store-connect-mcp web logout [--profile NAME]   delete the stored session and the dedicated browser profile

web login reuses a still-valid session unless --force. The browser is your default one when it is
Chromium-based (Chrome, Brave, Edge, ...), else another installed Chromium, else a downloaded
Chrome for Testing; ` + browser.EnvBrowser + `=PATH (or "` + browser.Bundled + `") or --browser PATH picks one.
The web session powers the opt-in "web" toolset (create app records, App Privacy).
MCP tools never see your Apple ID, password or cookies; the password and 2FA code are never stored.`

// webToolsHint tells the user how to turn the web toolset on.
const webToolsHint = `Web tools: start with --toolsets +web or call toolsets {"action":"enable","toolsets":["web"]}`

// Test hooks.
var (
	// webHTTPClient talks to Apple; nil uses a client with a 30 s timeout.
	webHTTPClient *http.Client
	webNow        = time.Now
	webStateDir   = state.DefaultDir
	webLogin      = websession.Login
	// webBrowserLogin signs in through a window of the dedicated browser profile.
	webBrowserLogin = websession.BrowserLogin
	// webHasDisplay reports whether a browser window can be shown.
	webHasDisplay = browser.HasDisplay
	// webReadPassword reads a password without echo from a terminal.
	webReadPassword = func(f *os.File) ([]byte, error) { return term.ReadPassword(int(f.Fd())) }
)

// maxImportBytes bounds `web import` input.
const maxImportBytes = 1 << 20

func webClient() *http.Client {
	if webHTTPClient != nil {
		return webHTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// webOptions are the flags shared by the web subcommands.
type webOptions struct {
	profile       string
	appleID       string
	team          int64
	passwordStdin bool
	password      bool
	force         bool
	timeout       time.Duration
	browserPath   string
}

func parseWebFlags(name string, args []string, stderr io.Writer, maxArgs int) (webOptions, []string, error) {
	var o webOptions
	var team string
	fs := flag.NewFlagSet("web "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.profile, "profile", "", "credentials profile the session belongs to")
	switch name {
	case "login":
		fs.BoolVar(&o.password, "password", false, "sign in with Apple ID and password instead of a browser window")
		fs.StringVar(&o.appleID, "apple-id", "", "Apple ID email for --password (asked when omitted)")
		fs.BoolVar(&o.passwordStdin, "password-stdin", false, "with --password: read the password from the first line of stdin")
		fs.BoolVar(&o.force, "force", false, "sign in again even if the stored session is still valid")
		fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "how long to wait for the browser sign-in")
		fs.StringVar(&o.browserPath, "browser", "", "Chromium-based browser executable or .app (default: "+browser.EnvBrowser+", then auto-detect)")
		fallthrough
	case "import":
		fs.StringVar(&team, "team", "", "App Store Connect team (provider) id to switch to")
	}
	if err := fs.Parse(args); err != nil {
		return o, nil, usageErr(err.Error())
	}
	if fs.NArg() > maxArgs {
		return o, nil, usageErr(webUsage)
	}
	if !o.password && (o.appleID != "" || o.passwordStdin) {
		return o, nil, usageErr("--apple-id and --password-stdin only apply to the password sign-in: add --password, or drop them to sign in in a browser window")
	}
	if o.timeout <= 0 && name == "login" {
		return o, nil, usageErr("--timeout must be positive")
	}
	if team != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(team), 10, 64)
		if err != nil || id <= 0 {
			return o, nil, usageErr("--team must be a numeric team (provider) id; `web team` lists them")
		}
		o.team = id
	}
	return o, fs.Args(), nil
}

func runWeb(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(webUsage)
	}
	if wantsHelp(args) {
		_, err := fmt.Fprintln(stdout, webUsage)
		return err
	}
	maxArgs := map[string]int{"login": 0, "import": 1, "status": 0, "team": 1, "logout": 0}
	n, ok := maxArgs[args[0]]
	if !ok {
		return usageErr(fmt.Sprintf("unknown web subcommand %q\n%s", args[0], webUsage))
	}
	o, rest, err := parseWebFlags(args[0], args[1:], stderr, n)
	if err != nil {
		return err
	}
	profile, err := webProfile(o.profile)
	if err != nil {
		return err
	}
	dir, err := webStateDir()
	if err != nil {
		return err
	}
	store := websession.DefaultStore(dir)
	switch args[0] {
	case "login":
		return webLoginCmd(ctx, o, profile, dir, store, stdin, stdout, stderr)
	case "import":
		return webImport(ctx, o, profile, store, rest, stdin, stdout)
	case "status":
		return webStatus(ctx, profile, store, stdout)
	case "team":
		return webTeam(ctx, profile, store, rest, stdout)
	default:
		if err := store.Delete(profile); err != nil {
			return err
		}
		profileDir := websession.BrowserProfileDir(dir, profile)
		if err := browser.RemoveProfile(profileDir); err != nil {
			return fmt.Errorf("web logout: remove browser profile %s: %w", profileDir, err)
		}
		_, err := fmt.Fprintf(stdout, "Signed out: deleted the web session of profile %s and its browser profile (%s).\n", profile, profileDir)
		return err
	}
}

// webProfile resolves the profile name like the other subcommands (flag >
// env > pin > default profile). A web session needs no API key, so an
// undefined --profile name and an empty config are fine ("default").
func webProfile(flagProfile string) (string, error) {
	r, err := config.Resolve(config.Options{Flags: config.Flags{Profile: flagProfile}})
	if err != nil {
		if flagProfile != "" {
			return flagProfile, nil
		}
		return "", err
	}
	if r.Profile.Name == "" {
		return "default", nil
	}
	return r.Profile.Name, nil
}

// webLoginCmd signs in (browser window by default, --password for SRP),
// lets the human pick a team and stores the session. A stored session that
// still validates is kept unless --force.
func webLoginCmd(ctx context.Context, o webOptions, profile, stateDir string, store websession.Store, stdin io.Reader, stdout, stderr io.Writer) error {
	if !o.force {
		if done, err := webAlreadySignedIn(ctx, o, profile, store, stdout); done || err != nil {
			return err
		}
	}
	in := bufio.NewReader(stdin)
	p := &prompter{in: in, out: stderr}
	tty := isTerminal(stdin)
	var sess *websession.Session
	var err error
	if o.password {
		sess, err = webPasswordLogin(ctx, o, in, p, tty, stdin, stderr)
	} else {
		sess, err = webBrowserSignIn(ctx, o, profile, stateDir, stderr)
	}
	if err != nil {
		return err
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = webNow()
	}
	info, err := websession.Validate(ctx, webClient(), sess, webNow())
	if err != nil {
		return fmt.Errorf("web login: check session: %w", err)
	}
	team := o.team
	if team == 0 && tty && len(info.Available) > 1 {
		if team, err = askTeam(p, info); err != nil {
			return err
		}
	}
	if info, err = applyTeam(ctx, sess, info, team); err != nil {
		return err
	}
	if err := store.Save(profile, sess); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Signed in as %s (team %s). %s\n", info.AppleID, teamLabel(info.Provider), webToolsHint)
	return err
}

// webAlreadySignedIn reports done=true when the stored session still
// validates; a --team that differs from its team is switched in place.
// A missing, expired or uncheckable session means sign in again.
func webAlreadySignedIn(ctx context.Context, o webOptions, profile string, store websession.Store, stdout io.Writer) (bool, error) {
	sess, err := store.Load(profile)
	if err != nil {
		return false, nil //nolint:nilerr // no usable stored session: sign in
	}
	info, err := websession.Validate(ctx, webClient(), sess, webNow())
	if err != nil {
		return false, nil //nolint:nilerr // expired or unreachable: sign in again
	}
	if o.team != 0 && o.team != info.Provider.ID {
		if info, err = applyTeam(ctx, sess, info, o.team); err != nil {
			return true, err
		}
		if err := store.Save(profile, sess); err != nil {
			return true, err
		}
	}
	_, err = fmt.Fprintf(stdout, "Already signed in as %s (team %s); `web login --force` signs in again. %s\n", info.AppleID, teamLabel(info.Provider), webToolsHint)
	return true, err
}

// webBrowserSignIn opens App Store Connect in the profile's dedicated
// browser profile and waits for the human to sign in.
func webBrowserSignIn(ctx context.Context, o webOptions, profile, stateDir string, stderr io.Writer) (*websession.Session, error) {
	if !webHasDisplay() {
		return nil, errors.New("web login: no display for a browser window; on a headless machine use `web login --password`, or sign in elsewhere and use `web import`")
	}
	sess, err := webBrowserLogin(ctx, websession.BrowserLoginOptions{
		ProfileDir:  websession.BrowserProfileDir(stateDir, profile),
		BrowserPath: o.browserPath,
		Timeout:     o.timeout,
		Log:         stderr,
		HTTP:        webClient(),
		Now:         webNow,
	})
	if err != nil {
		return nil, fmt.Errorf("web login: %w", err)
	}
	if sess.Source == "" {
		sess.Source = "browser"
	}
	return sess, nil
}

// webPasswordLogin signs in with Apple ID, password and 2FA (SRP).
func webPasswordLogin(ctx context.Context, o webOptions, in *bufio.Reader, p *prompter, tty bool, stdin io.Reader, stderr io.Writer) (*websession.Session, error) {
	if !tty && !o.passwordStdin {
		return nil, errors.New("web login --password needs a terminal to read the password without echo; pass --password-stdin to read it from stdin, or use `web import`")
	}
	appleID := strings.TrimSpace(o.appleID)
	var err error
	if appleID == "" {
		if appleID, err = p.ask("Apple ID (email)", ""); err != nil {
			return nil, err
		}
		if appleID == "" {
			return nil, errors.New("an Apple ID is required")
		}
	}
	password, err := readWebPassword(in, stdin, stderr, o.passwordStdin)
	if err != nil {
		return nil, err
	}
	sess, err := webLogin(ctx, websession.LoginOptions{
		AppleID:  appleID,
		Password: password,
		Code:     func(_ context.Context, c websession.TwoFactor) (string, error) { return askTwoFactor(p, c) },
		Now:      webNow,
	})
	if err != nil {
		return nil, fmt.Errorf("web login: %w", err)
	}
	if sess.Source == "" {
		sess.Source = "login"
	}
	return sess, nil
}

// readWebPassword reads the password without echo from a terminal, or the
// first stdin line with --password-stdin.
func readWebPassword(in *bufio.Reader, stdin io.Reader, stderr io.Writer, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("--password-stdin: no password on stdin")
		}
		return pw, nil
	}
	f, ok := stdin.(*os.File)
	if !ok {
		return "", errors.New("web login needs a terminal to read the password; pass --password-stdin")
	}
	fmt.Fprint(stderr, "Password (not shown, never stored): ")
	b, err := webReadPassword(f)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(b) == 0 {
		return "", errors.New("a password is required")
	}
	return string(b), nil
}

// askTwoFactor prompts for a 2FA code. Enter on an empty line at the device
// prompt lists the trusted phones; "sms N" texts phone N.
func askTwoFactor(p *prompter, c websession.TwoFactor) (string, error) {
	for {
		q := "Two-factor code shown on your Apple devices (Enter = text it to a phone)"
		if c.Kind == "sms" {
			q = "Two-factor code sent by SMS"
		}
		a, err := p.ask(q, "")
		if err != nil {
			return "", err
		}
		a = strings.TrimSpace(a)
		lower := strings.ToLower(a)
		switch {
		case a == "" || lower == "sms":
			if len(c.Phones) == 0 {
				fmt.Fprintln(p.out, "No trusted phone numbers are available; enter the code from your device.")
				continue
			}
			for i, ph := range c.Phones {
				fmt.Fprintf(p.out, "  %d. %s\n", i+1, ph)
			}
			if len(c.Phones) == 1 {
				return "sms:1", nil
			}
			n, err := p.ask("Text the code to which number", "1")
			if err != nil {
				return "", err
			}
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && i >= 1 && i <= len(c.Phones) {
				return "sms:" + strconv.Itoa(i), nil
			}
			fmt.Fprintf(p.out, "Enter a number from 1 to %d.\n", len(c.Phones))
		case strings.HasPrefix(lower, "sms "):
			i, err := strconv.Atoi(strings.TrimSpace(a[4:]))
			if err != nil || i < 1 || (len(c.Phones) > 0 && i > len(c.Phones)) {
				fmt.Fprintf(p.out, "Use sms N with N from 1 to %d.\n", max(len(c.Phones), 1))
				continue
			}
			return "sms:" + strconv.Itoa(i), nil
		default:
			code := strings.ReplaceAll(a, " ", "")
			if _, err := strconv.Atoi(code); err != nil || (c.CodeLength > 0 && len(code) != c.CodeLength) {
				want := c.CodeLength
				if want == 0 {
					want = 6
				}
				fmt.Fprintf(p.out, "The code has %d digits.\n", want)
				continue
			}
			return code, nil
		}
	}
}

// askTeam lists the available teams and asks which one to use.
func askTeam(p *prompter, info *websession.Info) (int64, error) {
	fmt.Fprintln(p.out, "Teams on this Apple ID:")
	for i, t := range info.Available {
		mark := " "
		if t.ID == info.Provider.ID {
			mark = "*"
		}
		fmt.Fprintf(p.out, " %s %d. %s\n", mark, i+1, teamLabel(t))
	}
	for {
		a, err := p.ask("Team number", "current")
		if err != nil {
			return 0, err
		}
		if a == "current" {
			return 0, nil
		}
		if i, err := strconv.Atoi(a); err == nil && i >= 1 && i <= len(info.Available) {
			return info.Available[i-1].ID, nil
		}
		fmt.Fprintf(p.out, "Enter a number from 1 to %d.\n", len(info.Available))
	}
}

// applyTeam switches sess to team (0 or current = no switch) and records
// the account and team Apple reports.
func applyTeam(ctx context.Context, sess *websession.Session, info *websession.Info, team int64) (*websession.Info, error) {
	if team != 0 && team != info.Provider.ID {
		found := len(info.Available) == 0
		for _, t := range info.Available {
			found = found || t.ID == team
		}
		if !found {
			return nil, fmt.Errorf("team %d is not available to %s (run `app-store-connect-mcp web team` to list teams)", team, info.AppleID)
		}
		if err := websession.SelectProvider(ctx, webClient(), sess, team, webNow()); err != nil {
			return nil, err
		}
		var err error
		if info, err = websession.Validate(ctx, webClient(), sess, webNow()); err != nil {
			return nil, fmt.Errorf("check session after switching team: %w", err)
		}
		if info.Provider.ID != team {
			return nil, fmt.Errorf("team switch did not take effect: asked for team %d, Apple reports %d", team, info.Provider.ID)
		}
	}
	sess.AppleID = info.AppleID
	sess.ProviderID = info.Provider.ID
	sess.ProviderName = info.Provider.Name
	return info, nil
}

func teamLabel(p websession.Provider) string {
	if p.Name == "" {
		return strconv.FormatInt(p.ID, 10)
	}
	return fmt.Sprintf("%s, id %d", p.Name, p.ID)
}

func webImport(ctx context.Context, o webOptions, profile string, store websession.Store, rest []string, stdin io.Reader, stdout io.Writer) error {
	r := stdin
	src := "stdin"
	if len(rest) == 1 && rest[0] != "-" {
		f, err := os.Open(rest[0]) //nolint:gosec // the human names the file to import on their own command line
		if err != nil {
			return fmt.Errorf("web import: %w", err)
		}
		defer func() { _ = f.Close() }()
		r, src = f, rest[0]
	}
	data, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return fmt.Errorf("web import: read %s: %w", src, err)
	}
	if len(data) > maxImportBytes {
		return fmt.Errorf("web import: %s is larger than 1 MiB", src)
	}
	sess, format, err := websession.Import(data)
	if err != nil {
		return err
	}
	sess.CreatedAt = webNow()
	info, err := websession.Validate(ctx, webClient(), sess, webNow())
	if errors.Is(err, websession.ErrExpired) {
		return errors.New("web import: Apple rejected these cookies (signed out or expired); sign in at https://appstoreconnect.apple.com in the browser and copy them again")
	}
	if err != nil {
		return fmt.Errorf("web import: check session: %w", err)
	}
	if info, err = applyTeam(ctx, sess, info, o.team); err != nil {
		return err
	}
	if err := store.Save(profile, sess); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Imported web session for profile %s (format %s, %d cookies): signed in as %s (team %s). %s\n",
		profile, format, len(sess.Cookies), info.AppleID, teamLabel(info.Provider), webToolsHint)
	return err
}

func webStatus(ctx context.Context, profile string, store websession.Store, stdout io.Writer) error {
	sess, err := store.Load(profile)
	if errors.Is(err, websession.ErrNoSession) {
		fmt.Fprintf(stdout, "web session (profile %s): none; run `app-store-connect-mcp web login` or `web import`\n", profile)
		return &guardExit{1}
	}
	if err != nil {
		return err
	}
	age := "unknown age"
	if !sess.CreatedAt.IsZero() {
		age = "created " + sess.CreatedAt.Local().Format("2006-01-02 15:04") + ", " + webNow().Sub(sess.CreatedAt).Round(time.Minute).String() + " ago"
	}
	fmt.Fprintf(stdout, "web session (profile %s): account %s, team %s, source %s, %s, %d cookies\n",
		profile, sess.AppleID, teamLabel(websession.Provider{ID: sess.ProviderID, Name: sess.ProviderName}), sess.Source, age, len(sess.Cookies))
	info, err := websession.Validate(ctx, webClient(), sess, webNow())
	switch {
	case errors.Is(err, websession.ErrExpired):
		fmt.Fprintln(stdout, "status: expired; run `app-store-connect-mcp web login` again")
		return &guardExit{1}
	case err != nil:
		fmt.Fprintf(stdout, "status: unknown (%v)\n", err)
		return &guardExit{1}
	}
	_, err = fmt.Fprintf(stdout, "status: active as %s (team %s)\n", info.AppleID, teamLabel(info.Provider))
	return err
}

func webTeam(ctx context.Context, profile string, store websession.Store, rest []string, stdout io.Writer) error {
	sess, err := store.Load(profile)
	if errors.Is(err, websession.ErrNoSession) {
		return fmt.Errorf("no web session for profile %s; run `app-store-connect-mcp web login` first", profile)
	}
	if err != nil {
		return err
	}
	info, err := websession.Validate(ctx, webClient(), sess, webNow())
	if err != nil {
		return fmt.Errorf("web team: %w", err)
	}
	if len(rest) == 0 {
		teams := info.Available
		if len(teams) == 0 {
			teams = []websession.Provider{info.Provider}
		}
		for _, t := range teams {
			mark := " "
			if t.ID == info.Provider.ID {
				mark = "*"
			}
			fmt.Fprintf(stdout, "%s %d  %s\n", mark, t.ID, t.Name)
		}
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(rest[0]), 10, 64)
	if err != nil || id <= 0 {
		return usageErr("web team ID: ID must be a numeric team (provider) id")
	}
	if info, err = applyTeam(ctx, sess, info, id); err != nil {
		return err
	}
	if err := store.Save(profile, sess); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Web session of profile %s now uses team %s.\n", profile, teamLabel(info.Provider))
	return err
}
