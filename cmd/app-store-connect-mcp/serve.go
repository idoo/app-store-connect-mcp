package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

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

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// envHTTPAllowedOrigins lists extra allowed Origin values, comma-separated.
const envHTTPAllowedOrigins = "ASC_MCP_HTTP_ALLOWED_ORIGINS"

// parseOrigins validates exact origins (scheme://host[:port]) from the
// --allow-origin flags and envHTTPAllowedOrigins. They are accepted only
// with --allow-remote, since loopback origins are always allowed.
func parseOrigins(flags []string, env string, allowRemote bool) ([]string, error) {
	raw := slices.Clone(flags)
	for _, v := range strings.Split(env, ",") {
		if v = strings.TrimSpace(v); v != "" {
			raw = append(raw, v)
		}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if !allowRemote {
		return nil, errors.New("--allow-origin and " + envHTTPAllowedOrigins + " need --allow-remote")
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return nil, fmt.Errorf("allowed origin %q: want an exact origin scheme://host[:port]", v)
		}
		out = append(out, v)
	}
	return out, nil
}

// serveArgs is the parsed `app-store-connect-mcp serve` command line.
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
	profile := fs.String("profile", "", "profile name (overrides ASC_MCP_PROFILE and .app-store-connect-mcp.toml)")
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
	maxConns := fs.Int("http-max-conns", 0, "cap open --http TCP connections; negative is unlimited (default "+mcpserver.EnvHTTPMaxConns+" or "+strconv.Itoa(mcpserver.DefaultMaxConns)+")")
	var origins stringList
	fs.Var(&origins, "allow-origin", "extra allowed browser Origin scheme://host[:port] for --http (repeatable; needs --allow-remote; also "+envHTTPAllowedOrigins+")")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return nil, usageErr("serve takes no arguments")
	}
	allowed, err := parseOrigins(origins, os.Getenv(envHTTPAllowedOrigins), *allowRemote)
	if err != nil {
		return nil, err
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
			AllowedOrigins: allowed,
			SessionTimeout: *sessionTimeout,
			MaxSessions:    *maxSessions,
			MaxConns:       *maxConns,
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
