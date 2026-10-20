package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// ServeOptions configures [Serve].
type ServeOptions struct {
	// Resolved is the effective configuration.
	Resolved *config.Resolved
	// Version is the app-store-connect-mcp build version.
	Version string
	// HTTPAddr serves Streamable HTTP when set; empty serves stdio.
	HTTPAddr string
	// HTTP holds the HTTP transport options (NewServer is filled in by Serve).
	HTTP mcpserver.HTTPOptions
	// Logger receives server logs on stderr; nil uses a stderr text logger.
	Logger *slog.Logger
	// Root confines file tools to this directory (--root); empty falls back
	// to ASC_MCP_ROOT, then the client's MCP roots, then the git root of the cwd.
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
	root := opts.Root
	if root == "" {
		root = os.Getenv("ASC_MCP_ROOT")
	}
	env, err := newEnv(ctx, r, opts.Version, root)
	if err != nil {
		return err
	}
	// After newEnv, which may add a state-dir problem.
	for _, p := range r.Profile.Problems {
		logger.Warn(p)
	}
	if pf, ok := env.Files.(*ProjectFiles); ok {
		defer func() { _ = pf.Close() }()
	}
	if _, none := env.ASC.(noCredentialsASC); none {
		logger.Warn(ErrNoProfile.Error())
	}
	deps := mcpserver.Deps{
		Env:            *env,
		Profile:        env.Profile,
		WritesLocked:   r.WritesLocked,
		Toolsets:       r.Toolsets,
		Discovery:      r.Discovery,
		Version:        opts.Version,
		Logger:         logger,
		MaxResultBytes: r.MaxResponseBytes,
	}
	if opts.HTTPAddr == "" {
		deps.Env.Transport = tool.TransportStdio
		srv, err := mcpserver.Build(ctx, deps)
		if err != nil {
			return fmt.Errorf("app: build server: %w", err)
		}
		return mcpserver.RunStdio(ctx, srv)
	}
	deps.Env.Transport = tool.TransportHTTP
	// Validate once up front so misconfiguration fails before listening.
	if _, err := mcpserver.Build(ctx, deps); err != nil {
		return fmt.Errorf("app: build server: %w", err)
	}
	h := opts.HTTP
	h.Logger = logger
	h.NewServer = mcpserver.SessionFactory(deps)
	h.NewStatelessServer = mcpserver.StatelessFactory(deps)
	return mcpserver.ServeHTTP(ctx, opts.HTTPAddr, h)
}
