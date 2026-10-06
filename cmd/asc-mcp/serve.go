package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strconv"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
)

// optionalBool is a flag that records whether it was given.
type optionalBool struct{ v *bool }

func (b *optionalBool) String() string {
	if b.v == nil {
		return ""
	}
	if *b.v {
		return "true"
	}
	return "false"
}

func (b *optionalBool) Set(s string) error {
	v := s == "" || s == "true" || s == "1"
	if !v && s != "false" && s != "0" {
		return errors.New("want true or false")
	}
	b.v = &v
	return nil
}

func (b *optionalBool) IsBoolFlag() bool { return true }

// optionalString is a flag that records whether it was given.
type optionalString struct{ v *string }

func (s *optionalString) String() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

func (s *optionalString) Set(v string) error { s.v = &v; return nil }

// serveArgs is the parsed `asc-mcp serve` command line.
type serveArgs struct {
	flags    config.Flags
	httpAddr string
	http     mcpserver.HTTPOptions
	root     string
}

// parseServeArgs parses serve flags; it returns nil, nil after printing help.
func parseServeArgs(args []string, out io.Writer) (*serveArgs, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	profile := fs.String("profile", "", "profile name (overrides ASC_MCP_PROFILE and .asc-mcp.toml)")
	var writes, discovery optionalBool
	var toolsets optionalString
	fs.Var(&writes, "allow-writes", "enable write tools for this session")
	fs.Var(&toolsets, "toolsets", `toolsets expression, e.g. "all" or "+signing,-reports"`)
	fs.Var(&discovery, "discovery", "list only core tools; enable others at runtime")
	noConfirm := fs.Bool("no-confirm", false, "skip previews (honored only with ASC_MCP_CI=1)")
	root := fs.String("root", "", "directory file tools may read and write (default: ASC_MCP_ROOT, the client's MCP roots, or the git root)")
	httpAddr := fs.String("http", "", "serve Streamable HTTP on ADDR (e.g. 127.0.0.1:8765) instead of stdio")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback --http address (requires TLS)")
	tlsCert := fs.String("tls-cert", "", "TLS certificate PEM for --http")
	tlsKey := fs.String("tls-key", "", "TLS private key PEM for --http")
	sessionTimeout := fs.Duration("http-session-timeout", 0, "close idle --http sessions after this long, e.g. 30m; negative never (default "+mcpserver.EnvHTTPSessionTimeout+" or "+mcpserver.DefaultSessionTimeout.String()+")")
	maxSessions := fs.Int("http-max-sessions", 0, "cap open --http sessions; negative is unlimited (default "+mcpserver.EnvHTTPMaxSessions+" or "+strconv.Itoa(mcpserver.DefaultMaxSessions)+")")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return nil, usageErr("serve takes no arguments")
	}
	return &serveArgs{
		flags: config.Flags{
			Profile:     *profile,
			AllowWrites: writes.v,
			Toolsets:    toolsets.v,
			Discovery:   discovery.v,
			NoConfirm:   *noConfirm,
		},
		httpAddr: *httpAddr,
		root:     *root,
		http: mcpserver.HTTPOptions{
			Token:          os.Getenv(mcpserver.EnvHTTPToken),
			AllowRemote:    *allowRemote,
			TLSCert:        *tlsCert,
			TLSKey:         *tlsKey,
			SessionTimeout: *sessionTimeout,
			MaxSessions:    *maxSessions,
		},
	}, nil
}

func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	sa, err := parseServeArgs(args, stderr)
	if sa == nil || err != nil {
		return err
	}
	r, err := config.Resolve(config.Options{Flags: sa.flags})
	if err != nil {
		return err
	}
	return app.Serve(ctx, app.ServeOptions{
		Resolved: r,
		Version:  buildVersion(),
		HTTPAddr: sa.httpAddr,
		HTTP:     sa.http,
		Root:     sa.root,
	})
}
