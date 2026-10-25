package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/mcpserver"
)

// ServeOptions configures [Serve].
type ServeOptions struct {
	// Resolved is the effective configuration.
	Resolved *config.Resolved
	// Version is the hamn build version.
	Version string
	// HTTPAddr serves Streamable HTTP when set; empty serves stdio.
	HTTPAddr string
	// HTTP holds the HTTP transport options (NewServer is filled in by Serve).
	HTTP mcpserver.HTTPOptions
	// Logger receives server logs on stderr; nil uses a stderr text logger.
	Logger *slog.Logger
	// Root confines file tools to this directory (--root); empty falls back
	// to HAMN_ROOT, then the client's MCP roots, then the git root of the cwd.
	Root string
}

// Serve builds the environment and runs the MCP server until ctx ends: one
// server over stdio, or one server per HTTP session.
func Serve(ctx context.Context, opts ServeOptions) error {
	r := opts.Resolved
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	for _, w := range r.Warnings {
		logger.Warn(w)
	}
	for _, p := range r.Profile.Problems {
		logger.Warn(p)
	}
	root := opts.Root
	if root == "" {
		root = os.Getenv("HAMN_ROOT")
	}
	env, err := newEnv(ctx, r, opts.Version, root)
	if err != nil {
		return err
	}
	if _, none := env.ASC.(noCredentialsASC); none {
		logger.Warn(ErrNoProfile.Error())
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("app: cwd: %w", err)
	}
	home, _ := os.UserHomeDir()
	locked, err := WritesLocked(cwd, home)
	if err != nil {
		return err
	}
	deps := mcpserver.Deps{
		Env:            *env,
		Profile:        env.Profile,
		WritesLocked:   locked,
		Toolsets:       r.Toolsets,
		Discovery:      r.Discovery,
		Version:        opts.Version,
		Logger:         logger,
		MaxResultBytes: r.MaxResponseBytes,
	}
	if opts.HTTPAddr == "" {
		srv, err := mcpserver.Build(ctx, deps)
		if err != nil {
			return fmt.Errorf("app: build server: %w", err)
		}
		return mcpserver.RunStdio(ctx, srv)
	}
	// Validate once up front so misconfiguration fails before listening.
	if _, err := mcpserver.Build(ctx, deps); err != nil {
		return fmt.Errorf("app: build server: %w", err)
	}
	h := opts.HTTP
	h.Logger = logger
	h.NewServer = mcpserver.SessionFactory(deps)
	return mcpserver.ServeHTTP(ctx, opts.HTTPAddr, h)
}
