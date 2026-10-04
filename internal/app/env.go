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

	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/auth"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/resolve"
	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/state"
	"github.com/idoo/hamn/internal/tool"
)

// ErrNoProfile means no credentials profile is configured.
var ErrNoProfile = errors.New("no App Store Connect profile configured (run `hamn auth add` or set ASC_KEY_ID)")

// errNoCredentials is what every App Store Connect call returns when no
// credentials are configured. GUI clients hide hamn's stderr, so the tool
// error itself carries the setup steps.
var errNoCredentials = &tool.Error{
	Code:    "no_credentials",
	Message: "no App Store Connect API key is configured for hamn",
	Hint: "in a terminal run `hamn auth add` (or `hamn auth import` to copy ASC_* env vars into a profile), then restart this client; " +
		"or set ASC_KEY_ID, ASC_ISSUER_ID and ASC_PRIVATE_KEY_PATH in this client's hamn server env. " +
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
		UserAgent:   "hamn/" + version,
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
// HAMN_ROOT when set (see [ProjectFiles]).
func NewEnv(ctx context.Context, r *config.Resolved, version string) (*tool.Env, error) {
	return newEnv(ctx, r, version, os.Getenv("HAMN_ROOT"))
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
	dir, err := state.DefaultDir()
	if err != nil {
		return nil, fmt.Errorf("app: state dir: %w", err)
	}
	store, err := state.New(dir)
	if err != nil {
		return nil, fmt.Errorf("app: state: %w", err)
	}
	home, _ := os.UserHomeDir()
	files, err := NewProjectFiles(root, cwd, home)
	if err != nil {
		return nil, err
	}
	env := &tool.Env{
		Spec:      idx,
		State:     store,
		Fetch:     asc.NewFetcher(asc.FetchOptions{UserAgent: "hamn/" + version}),
		Files:     files,
		Clock:     tool.SystemClock{},
		Profile:   r.Profile.Info(),
		Version:   version,
		NoConfirm: r.NoConfirm && r.CI,
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
