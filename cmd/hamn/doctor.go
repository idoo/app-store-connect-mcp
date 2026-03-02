package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/app"
	"github.com/idoo/hamn/internal/auth"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
)

// maxClockSkew is the skew beyond which JWTs may be rejected.
const maxClockSkew = time.Minute

// report collects doctor lines and remembers failures.
type report struct {
	w      io.Writer
	failed bool
}

func (r *report) ok(format string, a ...any)   { r.line("ok  ", format, a...) }
func (r *report) warn(format string, a ...any) { r.line("warn", format, a...) }
func (r *report) fail(format string, a ...any) {
	r.failed = true
	r.line("FAIL", format, a...)
}

func (r *report) line(tag, format string, a ...any) {
	fmt.Fprintf(r.w, "%s  %s\n", tag, fmt.Sprintf(format, a...))
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile to check")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err.Error())
	}
	rep := &report{w: stdout}
	doctor(ctx, rep, *profile)
	if rep.failed {
		return errors.New("doctor found problems")
	}
	return nil
}

func doctor(ctx context.Context, rep *report, profile string) {
	fmt.Fprintf(rep.w, "hamn %s\n", buildVersion())
	if idx, err := spec.Load(); err != nil {
		rep.fail("spec: %v", err)
	} else {
		rep.ok("spec %s (%d paths)", idx.Version(), idx.PathCount())
	}
	r, err := config.Resolve(config.Options{Flags: config.Flags{Profile: profile}})
	if err != nil {
		rep.fail("config: %v", err)
		return
	}
	for _, w := range r.Warnings {
		rep.warn("%s", w)
	}
	for _, p := range r.Profile.Problems {
		rep.warn("%s", p)
	}
	p := r.Profile
	if p.KeyID == "" {
		rep.fail("%v", app.ErrNoProfile)
		return
	}
	line := fmt.Sprintf("profile %q key %s (%s)", p.Name, p.KeyID, p.Key)
	if p.PinSource != "" {
		line += " selected by " + p.PinSource
	}
	rep.ok("%s", line)
	switch p.KeyKind {
	case tool.KeyTeam:
		rep.ok("key kind: team")
	case tool.KeyIndividual:
		rep.warn("key kind: individual (sales, finance and signing tools are unavailable)")
	default:
		rep.warn("key kind undetermined: set issuer_id, or `hamn auth set %s key_kind=individual`", p.Name)
	}
	if p.VendorNumber == "" && p.KeyKind != tool.KeyIndividual {
		rep.warn("no vendor number: sales and finance reports need it (`hamn auth set %s vendor_number=...`)", p.Name)
	}
	if !signCheck(ctx, rep, r) {
		return
	}
	c, resp, err := probeApple(ctx, r)
	if err != nil {
		rep.fail("Apple API: %v", err)
	} else {
		rep.ok("Apple API reachable (GET /v1/apps: %d)", resp.Status)
	}
	if c != nil {
		if skew, ok := c.ClockSkew(); ok {
			if skew.Abs() > maxClockSkew {
				rep.warn("clock skew %s vs Apple: JWTs may be rejected; sync the system clock", skew.Round(time.Second))
			} else {
				rep.ok("clock skew %s", skew.Round(time.Second))
			}
		}
		if rl := c.RateLimit(); rl.Known {
			if rl.Remaining < 200 {
				rep.warn("rate limit low: %d/%d remaining this hour", rl.Remaining, rl.Limit)
			} else {
				rep.ok("rate limit %d/%d remaining this hour", rl.Remaining, rl.Limit)
			}
		}
	}
	if p.AllowWrites {
		rep.ok("mode: writes enabled")
	} else {
		rep.ok("mode: read-only (use --allow-writes or allow_writes=true)")
	}
	names := make([]string, len(r.Toolsets))
	for i, ts := range r.Toolsets {
		names[i] = string(ts)
	}
	if r.Discovery {
		rep.ok("toolsets: discovery mode (core listed; others enabled at runtime)")
	} else {
		rep.ok("toolsets: %s", strings.Join(names, ", "))
	}
}

// signCheck loads the key and signs a JWT.
func signCheck(ctx context.Context, rep *report, r *config.Resolved) bool {
	src, err := app.KeySource(r.Profile.Key, os.Getenv)
	if err == nil {
		var key *auth.Key
		if key, err = auth.ResolveKey(ctx, r.Profile.KeyID, src); err == nil {
			issuer := r.Profile.IssuerID
			if r.Profile.KeyKind == tool.KeyIndividual {
				issuer = ""
			}
			var s *auth.Signer
			if s, err = auth.NewSigner(key, issuer); err == nil {
				_, err = s.Sign(time.Now())
			}
		}
	}
	if err != nil {
		rep.fail("key/JWT: %v", err)
		return false
	}
	rep.ok("key loaded and JWT signed")
	return true
}
