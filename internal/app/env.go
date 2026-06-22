// Package app wires configuration, credentials, the App Store Connect client
// and local state into a [tool.Env], and runs the MCP server.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

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
// The client permits non-GET requests unless writesLocked: the session's write
// mode (which can be switched on at runtime after the human approves) and the
// write context set by tool.Call remain the gates for every mutation.
func NewClient(ctx context.Context, r *config.Resolved, version string, idx *spec.Index, writesLocked bool) (*asc.Client, error) {
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
		AllowWrites: p.AllowWrites || !writesLocked,
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

// WritesLocked reports whether the project pin in cwd forbids writes
// (allow_writes = false), which also forbids enabling them at runtime.
func WritesLocked(cwd, home string) (bool, error) {
	pin, err := config.FindPin(cwd, home)
	if err != nil {
		return false, fmt.Errorf("app: pin: %w", err)
	}
	return pin != nil && pin.AllowWrites != nil && !*pin.AllowWrites, nil
}

// NewEnv builds the base tool environment for resolved. Without a configured
// profile the env has no ASC client or resolver; tools then fail with
// tool.ErrUnavailable and doctor explains the setup. Key-loading failures for a
// configured profile are returned as errors.
func NewEnv(ctx context.Context, r *config.Resolved, version string) (*tool.Env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("app: cwd: %w", err)
	}
	home, _ := os.UserHomeDir()
	locked, err := WritesLocked(cwd, home)
	if err != nil {
		return nil, err
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
	files, err := NewFiles(DefaultRoot(cwd))
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
	client, err := NewClient(ctx, r, version, idx, locked)
	switch {
	case errors.Is(err, ErrNoProfile):
	case err != nil:
		return nil, err
	default:
		env.ASC = client
		env.Resolve = resolve.New(client, resolve.Options{DefaultApp: r.Profile.DefaultApp, Clock: env.Clock})
	}
	return env, nil
}
