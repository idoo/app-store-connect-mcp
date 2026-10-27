// Package app wires configuration, credentials, the App Store Connect client
// and local state into a [tool.Env], and runs the MCP server.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/auth"
	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/spec"
	"github.com/idoo/app-store-connect-mcp/internal/state"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// ErrNoProfile means no credentials profile is configured.
var ErrNoProfile = errors.New("no App Store Connect profile configured (run `app-store-connect-mcp auth add` or set ASC_KEY_ID)")

// errNoCredentials is what every App Store Connect call returns when no
// credentials are configured. GUI clients hide app-store-connect-mcp's stderr, so the tool
// error itself carries the setup steps.
var errNoCredentials = &tool.Error{
	Code:    "no_credentials",
	Message: "no App Store Connect API key is configured for app-store-connect-mcp",
	Hint: "in a terminal run `app-store-connect-mcp auth add` (or `app-store-connect-mcp auth import` to copy ASC_* env vars into a profile), then restart this client; " +
		"or set ASC_KEY_ID, ASC_ISSUER_ID and ASC_PRIVATE_KEY_PATH in this client's app-store-connect-mcp server env. " +
		"GUI clients (Claude Desktop, Cursor, Windsurf) do not see your shell environment",
}

// noCredentialsASC stands in for the client when no key is configured.
type noCredentialsASC struct{}

func (noCredentialsASC) Do(context.Context, tool.Request) (*tool.Response, error) {
	return nil, errNoCredentials
}

func (noCredentialsASC) Get(context.Context, string, url.Values) (*tool.Response, error) {
	return nil, errNoCredentials
}

func (noCredentialsASC) List(context.Context, string, tool.ListOptions) (*tool.ListResult, error) {
	return nil, errNoCredentials
}

func (noCredentialsASC) Download(context.Context, tool.Request) ([]byte, http.Header, error) {
	return nil, nil, errNoCredentials
}
func (noCredentialsASC) RateLimit() tool.RateLimit        { return tool.RateLimit{} }
func (noCredentialsASC) ClockSkew() (time.Duration, bool) { return 0, false }

// KeySource maps a config key reference to the auth package's key source.
// Keychain keys are read lazily by auth; file keys by path; env and fastlane
// keys are read now and held only as an inline source.
func KeySource(ref config.KeyRef, getenv func(string) string) (auth.KeySource, error) {
	switch ref.Source {
	case config.KeyFromFile:
		return auth.FileSource{Path: ref.Path}, nil
	case config.KeyFromKeychain:
		return auth.KeychainSource{KeyID: ref.KeyID}, nil
	case config.KeyFromEnv, config.KeyFromFastlaneJSON:
		b, err := ref.Read(getenv)
		if err != nil {
			return nil, fmt.Errorf("app: read key %s: %w", ref, err)
		}
		return auth.InlineSource{Data: string(b), Origin: ref.String()}, nil
	default:
		return nil, fmt.Errorf("app: unknown key source %q", ref.Source)
	}
}

// NewClient loads the profile's key and builds the App Store Connect client.
// idx may be nil (no spec-derived error hints or free-text wrapping).
//
// The client's read-only guard starts at the profile's write mode; NewEnv
// wires tool.Env.SetWrites to [asc.Client.SetAllowWrites] so a runtime
// enable_writes (after the human approves) opens it. The write context set by
// tool.Call remains a second gate for every mutation.
func NewClient(ctx context.Context, r *config.Resolved, version string, idx *spec.Index) (*asc.Client, error) {
	p := r.Profile
	if p.KeyID == "" {
		return nil, ErrNoProfile
	}
	src, err := KeySource(p.Key, os.Getenv)
	if err != nil {
		return nil, err
	}
	key, err := auth.ResolveKey(ctx, p.KeyID, src)
	if err != nil {
		return nil, fmt.Errorf("app: load key: %w", err)
	}
	issuer := p.IssuerID
	if p.KeyKind == tool.KeyIndividual {
		issuer = ""
	}
	signer, err := auth.NewSigner(key, issuer)
	if err != nil {
		return nil, fmt.Errorf("app: signer: %w", err)
	}
	opts := asc.Options{
		Tokens:      auth.NewTokenSource(signer),
		MaxRetries:  r.MaxRetries,
		AllowWrites: p.AllowWrites,
		Clock:       tool.SystemClock{},
		UserAgent:   "app-store-connect-mcp/" + version,
	}
	if idx != nil {
		opts.Spec = idx
		opts.FreeText = idx.FreeTextAttributes
	}
	if r.BaseURL != "" {
		u, err := url.Parse(r.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("app: base url: %w", err)
		}
		opts.BaseURL = u
	}
	c, err := asc.New(opts)
	if err != nil {
		return nil, fmt.Errorf("app: client: %w", err)
	}
	return c, nil
}

// NewEnv builds the base tool environment for resolved. Without a configured
// profile the env has no ASC client or resolver; tools then fail with
// tool.ErrUnavailable and doctor explains the setup. Key-loading failures for a
// configured profile are returned as errors. The file root comes from
// ASC_MCP_ROOT when set (see [ProjectFiles]).
func NewEnv(ctx context.Context, r *config.Resolved, version string) (*tool.Env, error) {
	return newEnv(ctx, r, version, os.Getenv("ASC_MCP_ROOT"), "")
}

// newEnv builds the env; transport is tool.TransportStdio, TransportHTTP or
// "" (CLI subcommands). Only stdio gets [tool.Env.WebLogin]: a browser window
// opens on the server's machine, which is the human's only over stdio.
func newEnv(ctx context.Context, r *config.Resolved, version, root, transport string) (*tool.Env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: cwd: %w", err)
	}
	idx, err := spec.Load()
	if err != nil {
		return nil, fmt.Errorf("app: spec: %w", err)
	}
	store, stateDir, problem := OpenState()
	if problem != "" {
		r.Profile.Problems = append(r.Profile.Problems, problem)
	}
	home, _ := os.UserHomeDir()
	files, err := NewProjectFiles(root, cwd, home)
	if err != nil {
		return nil, err
	}
	env := &tool.Env{
		Spec:      idx,
		Fetch:     asc.NewFetcher(asc.FetchOptions{UserAgent: "app-store-connect-mcp/" + version}),
		Files:     files,
		Clock:     tool.SystemClock{},
		Profile:   r.Profile.Info(),
		Version:   version,
		NoConfirm: r.NoConfirm && r.CI,
	}
	// A nil *state.Store must stay a nil interface so tool.Env installs its
	// unavailable stand-in: cursor and resume use fail, everything else works.
	if store != nil {
		env.State = store
	}
	client, err := NewClient(ctx, r, version, idx)
	switch {
	case errors.Is(err, ErrNoProfile):
		env.ASC = noCredentialsASC{}
		env.Resolve = resolve.New(env.ASC, resolve.Options{DefaultApp: r.Profile.DefaultApp, Clock: env.Clock})
	case err != nil:
		return nil, err
	default:
		env.ASC = client
		env.Resolve = resolve.New(client, resolve.Options{DefaultApp: r.Profile.DefaultApp, Clock: env.Clock})
	}
	web, err := NewWebClient(r, version, idx, stateDir)
	if err != nil {
		return nil, err
	}
	if web != nil {
		env.Web = web
	}
	if transport == tool.TransportStdio && stateDir != "" {
		env.WebLogin = newWebLogin(stateDir, WebProfile(r))
	}
	env.SetWrites = func(on bool) {
		if client != nil {
			client.SetAllowWrites(on)
		}
		if web != nil {
			web.SetAllowWrites(on)
		}
	}
	return env, nil
}

// WebProfile is the name a web session is stored under for r: the
// credentials profile, or "default" when none exists (a web session needs
// no API key). It matches `app-store-connect-mcp web`.
func WebProfile(r *config.Resolved) string {
	if r.Profile.Name == "" {
		return "default"
	}
	return r.Profile.Name
}

// NewWebClient builds the web-session (iris) client. It loads the stored
// session lazily on first use, so a `web login` after the server started
// works without a restart. It returns nil, nil when the state dir is unknown
// (the session store lives under it); web tools then fail with
// tool.ErrWebSessionRequired.
func NewWebClient(r *config.Resolved, version string, idx *spec.Index, stateDir string) (*asc.Client, error) {
	if stateDir == "" {
		return nil, nil
	}
	base, err := url.Parse(websession.IrisBaseURL)
	if err != nil {
		return nil, fmt.Errorf("app: web base url: %w", err)
	}
	opts := asc.Options{
		BaseURL:     base,
		Session:     newWebSource(WebProfile(r), stateDir),
		HTTP:        webHTTPClient(),
		MaxRetries:  r.MaxRetries,
		AllowWrites: r.Profile.AllowWrites,
		Clock:       tool.SystemClock{},
		UserAgent:   "app-store-connect-mcp/" + version,
	}
	if idx != nil {
		opts.FreeText = idx.FreeTextAttributes
	}
	c, err := asc.New(opts)
	if err != nil {
		return nil, fmt.Errorf("app: web client: %w", err)
	}
	return c, nil
}

// webHTTPClient never follows a redirect off the iris API (Apple answers some
// expired sessions with a redirect to a sign-in page): the redirect response
// itself is returned and surfaces as an error instead of an HTML 200.
func webHTTPClient() *http.Client {
	return &http.Client{
		Timeout: asc.DefaultRequestTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Host != "appstoreconnect.apple.com" || len(req.URL.Path) < 6 || req.URL.Path[:6] != "/iris/" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// Test hooks for the browser sign-in.
var (
	webBrowserLogin = websession.BrowserLogin
	webHasDisplay   = browser.HasDisplay
	webValidate     = websession.Validate
)

// webRenewTimeout bounds one silent (headless) session renewal.
const webRenewTimeout = 60 * time.Second

// newWebSource is the profile's stored session. When Apple rejects it, or
// none is stored, it renews silently by running the dedicated browser
// profile headless, but only if an earlier browser sign-in created that
// profile; otherwise the human must sign in.
func newWebSource(profile, stateDir string) *websession.Source {
	src := websession.NewSource(profile, websession.DefaultStore(stateDir), nil)
	dir := websession.BrowserProfileDir(stateDir, profile)
	src.SetRenewer(func(ctx context.Context) (*websession.Session, error) {
		if !browser.ProfileExists(dir) {
			return nil, fmt.Errorf("no browser sign-in to renew for profile %s: %w", profile, tool.ErrWebSessionRequired)
		}
		return webBrowserLogin(ctx, websession.BrowserLoginOptions{
			ProfileDir: dir,
			Headless:   true,
			Timeout:    webRenewTimeout,
			Log:        io.Discard,
		})
	})
	return src
}

// newWebLogin is [tool.Env.WebLogin] for profile: it keeps a stored session
// that still validates, else opens a browser window of the dedicated profile
// for the human to sign in, and stores the session.
func newWebLogin(stateDir, profile string) func(context.Context, func(string)) (tool.WebAccount, error) {
	store := websession.DefaultStore(stateDir)
	return func(ctx context.Context, log func(string)) (tool.WebAccount, error) {
		hc := &http.Client{Timeout: 30 * time.Second}
		if sess, err := store.Load(profile); err == nil {
			if info, err := webValidate(ctx, hc, sess, time.Now()); err == nil {
				return tool.WebAccount{AppleID: info.AppleID, TeamID: info.Provider.ID, TeamName: info.Provider.Name, Reused: true}, nil
			}
		}
		if !webHasDisplay() {
			return tool.WebAccount{}, errors.New("no display for a browser window on this machine: ask the human to run `app-store-connect-mcp web login` in a terminal")
		}
		sess, err := webBrowserLogin(ctx, websession.BrowserLoginOptions{
			ProfileDir: websession.BrowserProfileDir(stateDir, profile),
			Log:        &lineWriter{fn: log},
			HTTP:       hc,
		})
		if err != nil {
			return tool.WebAccount{}, fmt.Errorf("web sign-in: %w", err)
		}
		if sess.CreatedAt.IsZero() {
			sess.CreatedAt = time.Now()
		}
		if err := store.Save(profile, sess); err != nil {
			return tool.WebAccount{}, fmt.Errorf("web sign-in: save session: %w", err)
		}
		return tool.WebAccount{AppleID: sess.AppleID, TeamID: sess.ProviderID, TeamName: sess.ProviderName}, nil
	}
}

// lineWriter forwards each complete, non-empty line written to it to fn.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := string(bytes.TrimSpace(w.buf[:i])); line != "" && w.fn != nil {
			w.fn(line)
		}
		w.buf = w.buf[i+1:]
	}
}

// OpenState opens the state store in its default directory and checks that
// the directory is writable. When it isn't, the store is nil and problem
// explains which features are unavailable; the server still starts.
func OpenState() (store *state.Store, dir, problem string) {
	dir, err := state.DefaultDir()
	if err != nil {
		return nil, "", fmt.Sprintf("state dir: %v (mark_seen, upload resume, digest and rank history unavailable)", err)
	}
	if store, err = state.New(dir); err == nil {
		err = probeWritable(dir)
	}
	if err != nil {
		return nil, dir, StateDirProblem(dir)
	}
	return store, dir, ""
}

// StateDirProblem is the doctor and log line for an unwritable state dir.
func StateDirProblem(dir string) string {
	return "state dir " + dir + ": not writable (mark_seen, upload resume, digest and rank history unavailable)"
}

// probeWritable creates and removes a temporary file in dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("app: state dir %s: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("app: state dir %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("app: state dir %s: %w", dir, err)
	}
	return nil
}
