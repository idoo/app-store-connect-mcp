package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/auth"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/spec"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
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
	asJSON := fs.Bool("json", false, "print the doctor tool's data object as JSON instead of text")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err.Error())
	}
	if *asJSON {
		return doctorJSON(ctx, stdout, *profile)
	}
	rep := &report{w: stdout}
	doctor(ctx, rep, *profile)
	if rep.failed {
		return errors.New("doctor found problems")
	}
	return nil
}

func doctor(ctx context.Context, rep *report, profile string) {
	fmt.Fprintf(rep.w, "app-store-connect-mcp %s\n", buildVersion())
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
	emptyKey := false
	for _, p := range r.Profile.Problems {
		if p == config.ProblemEmptyKey {
			emptyKey = true
			continue
		}
		rep.warn("%s", p)
	}
	if _, dir, problem := app.OpenState(); problem != "" {
		rep.warn("%s", problem)
	} else {
		rep.ok("state dir %s", dir)
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
	if emptyKey {
		rep.fail("%s: %s", line, config.ProblemEmptyKey)
	} else {
		rep.ok("%s", line)
	}
	if saved := shadowedProfile(r); saved != "" {
		rep.warn("ASC_* env overrides saved profile %q (unset %s to use it, or pass --profile %s)", saved, config.EnvKeyID, saved)
	}
	if p.Key.Source == config.KeyFromFile {
		if id, bad := keyIDMismatch(p.KeyID, p.Key.Path); bad {
			rep.warn("key id %s doesn't match the file name AuthKey_%s.p8; one of them is wrong", p.KeyID, id)
		}
	}
	if p.IssuerID != "" && !uuidRe.MatchString(p.IssuerID) {
		rep.warn("issuer id %q doesn't look like Apple's UUID (%s)", p.IssuerID, issuerHelp)
	}
	switch p.KeyKind {
	case tool.KeyTeam:
		rep.ok("key kind: team")
	case tool.KeyIndividual:
		rep.warn("key kind: individual (sales, finance and signing tools are unavailable)")
	default:
		rep.warn("key kind undetermined: set issuer_id, or `app-store-connect-mcp auth set %s key_kind=individual`", p.Name)
	}
	if p.VendorNumber == "" && p.KeyKind != tool.KeyIndividual {
		if p.Name == config.EnvProfileName {
			rep.warn("no vendor number: sales and finance reports need it (set %s)", config.EnvVendorNumber)
		} else {
			rep.warn("no vendor number: sales and finance reports need it (`app-store-connect-mcp auth set %s vendor_number=...`)", p.Name)
		}
	}
	if !signCheck(ctx, rep, r) {
		return
	}
	c, resp, err := probeApple(ctx, r)
	if err != nil {
		rep.fail("Apple API: %v%s", err, authChecklist(err, p))
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
	if p.AllowNoElicit {
		if r.CI {
			rep.ok("human gates: agent-approved (CI)")
		} else {
			rep.ok("human gates: agent-approved (allow_no_elicit)")
		}
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

// shadowedProfile names the saved profile that ASC_* environment credentials
// replace: the default profile, else the first by name. It is "" when no env
// profile is in use or nothing is saved.
func shadowedProfile(r *config.Resolved) string {
	if r.Profile.Name != config.EnvProfileName || r.Config == nil {
		return ""
	}
	if r.Config.DefaultProfile != "" {
		return r.Config.DefaultProfile
	}
	if names := slices.Sorted(maps.Keys(r.Config.Profiles)); len(names) > 0 {
		return names[0]
	}
	return ""
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
		if errors.Is(err, fs.ErrNotExist) && !strings.Contains(err.Error(), config.MissingKeyHint) {
			rep.fail("key/JWT: %v; %s", err, config.MissingKeyHint)
		} else {
			rep.fail("key/JWT: %v", err)
		}
		return false
	}
	rep.ok("key loaded and JWT signed")
	return true
}

// doctorJSON prints the MCP doctor tool's data object. The exit status
// matches text mode: an error when a check fails or no profile is usable.
func doctorJSON(ctx context.Context, stdout io.Writer, profile string) error {
	r, err := config.Resolve(config.Options{Flags: config.Flags{Profile: profile}})
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	env, err := app.NewEnv(ctx, r, buildVersion())
	if err != nil {
		return err
	}
	env.Toolsets = staticToolsets(r.Toolsets)
	t, ok := tool.Lookup("doctor")
	if !ok {
		return errors.New("doctor tool is not registered")
	}
	res := t.Call(ctx, env, nil)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res.Data); err != nil {
		return fmt.Errorf("doctor: encode: %w", err)
	}
	data := buf.Bytes()
	if _, err := stdout.Write(data); err != nil {
		return err
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("doctor: decode: %w", err)
	}
	if res.IsError || out.Status == "fail" || r.Profile.KeyID == "" || slices.Contains(r.Profile.Problems, config.ProblemEmptyKey) {
		return errors.New("doctor found problems")
	}
	return nil
}

// staticToolsets reports the configured toolsets to the doctor tool outside
// an MCP session; it cannot change them.
type staticToolsets []tool.Toolset

func (s staticToolsets) List() []tool.ToolsetInfo {
	out := make([]tool.ToolsetInfo, 0, len(tool.AllToolsets))
	for _, ts := range tool.AllToolsets {
		out = append(out, tool.ToolsetInfo{Name: ts, Enabled: slices.Contains(s, ts), Default: slices.Contains(tool.DefaultToolsets, ts)})
	}
	return out
}

func (staticToolsets) Enable(context.Context, ...tool.Toolset) error {
	return errors.New("toolsets can't change outside a server session")
}

func (staticToolsets) Disable(context.Context, ...tool.Toolset) error {
	return errors.New("toolsets can't change outside a server session")
}
