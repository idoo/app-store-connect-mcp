package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/idoo/hamn/internal/app"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/mcpserver"
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

func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name (overrides HAMN_PROFILE and .hamn.toml)")
	var writes, discovery optionalBool
	var toolsets optionalString
	fs.Var(&writes, "allow-writes", "enable write tools for this session")
	fs.Var(&toolsets, "toolsets", `toolsets expression, e.g. "all" or "+signing,-reports"`)
	fs.Var(&discovery, "discovery", "list only core tools; enable others at runtime")
	noConfirm := fs.Bool("no-confirm", false, "skip previews (honored only with HAMN_CI=1)")
	httpAddr := fs.String("http", "", "serve Streamable HTTP on ADDR (e.g. 127.0.0.1:8765) instead of stdio")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback --http address (requires TLS)")
	tlsCert := fs.String("tls-cert", "", "TLS certificate PEM for --http")
	tlsKey := fs.String("tls-key", "", "TLS private key PEM for --http")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return usageErr("serve takes no arguments")
	}
	r, err := config.Resolve(config.Options{Flags: config.Flags{
		Profile:     *profile,
		AllowWrites: writes.v,
		Toolsets:    toolsets.v,
		Discovery:   discovery.v,
		NoConfirm:   *noConfirm,
	}})
	if err != nil {
		return err
	}
	return app.Serve(ctx, app.ServeOptions{
		Resolved: r,
		Version:  buildVersion(),
		HTTPAddr: *httpAddr,
		HTTP: mcpserver.HTTPOptions{
			Token:       os.Getenv(mcpserver.EnvHTTPToken),
			AllowRemote: *allowRemote,
			TLSCert:     *tlsCert,
			TLSKey:      *tlsKey,
		},
	})
}
