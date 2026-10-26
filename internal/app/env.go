// Package app wires configuration, credentials, the App Store Connect client
// and local state into a [tool.Env], and runs the MCP server.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/auth"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/spec"
	"github.com/idoo/app-store-connect-mcp/internal/state"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
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
	return newEnv(ctx, r, version, os.Getenv("ASC_MCP_ROOT"))
}

func newEnv(ctx context.Context, r *config.Resolved, version, root string) (*tool.Env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: cwd: %w", err)
	}
	idx, err := spec.Load()
	if err != nil {
		return nil, fmt.Errorf("app: spec: %w", err)
	}
	store, _, problem := OpenState()
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
		env.SetWrites = client.SetAllowWrites
		env.Resolve = resolve.New(client, resolve.Options{DefaultApp: r.Profile.DefaultApp, Clock: env.Clock})
	}
	return env, nil
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
