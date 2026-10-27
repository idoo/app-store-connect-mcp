package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Check statuses, ordered by severity.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

// Clock skew thresholds: JWTs are back-dated 30 s, so a minute of skew can
// get tokens rejected.
const (
	skewWarn = 10 * time.Second
	skewFail = time.Minute
)

type doctorIn struct{}

// check is one doctor finding.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type doctorOut struct {
	Status      string   `json:"status"`
	Version     string   `json:"version,omitempty"`
	Profile     string   `json:"profile"`
	KeyKind     string   `json:"key_kind"`
	Writes      bool     `json:"writes"`
	SpecVersion string   `json:"spec_version,omitempty"`
	Toolsets    []string `json:"toolsets"`
	// FileRoot is the directory file tools may read and write; empty means
	// file access is refused (see the files check).
	FileRoot string  `json:"file_root"`
	Checks   []check `json:"checks"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "doctor", Toolset: tool.Core, Class: tool.Read, Idempotent: true,
		Title: "Check setup",
		Description: "Health check of the active profile: key, JWT, Apple reachability, clock skew, rate limit, spec, " +
			"write mode, toolsets, vendor number, each with a fix. Run first on 401/403.",
	}, doctor))
}

func doctor(ctx context.Context, env *tool.Env, _ doctorIn) (*tool.Result, error) {
	p := env.Profile
	out := doctorOut{
		Version: env.Version, Profile: p.Name, KeyKind: string(p.KeyKind),
		Writes: p.AllowWrites, SpecVersion: env.Spec.Version(), Toolsets: []string{},
	}
	add := func(c check) { out.Checks = append(out.Checks, c) }

	profile := check{Name: "profile", Status: statusOK, Detail: "using profile " + quoteOr(p.Name, "(env)")}
	if p.PinSource != "" {
		profile.Detail += " selected by " + p.PinSource
	}
	add(profile)
	add(keyKindCheck(p.KeyKind))
	for _, c := range reachabilityChecks(ctx, env) {
		add(c)
	}
	add(clockCheck(env.ASC))
	add(rateCheck(env.ASC.RateLimit()))
	add(specCheck(out.SpecVersion))
	add(writesCheck(p.AllowWrites))
	add(vendorCheck(p))
	for _, msg := range p.Problems {
		add(check{Name: "config", Status: statusWarn, Detail: msg})
	}
	if p.AllowNoElicit {
		add(check{Name: "human_gates", Status: statusWarn, Detail: "human approval is off: the agent approves public, irreversible and money actions (allow_no_elicit)"})
	}
	root, files := filesCheck(env.Files)
	out.FileRoot = root
	add(files)

	for _, ts := range env.Toolsets.List() {
		if ts.Enabled {
			out.Toolsets = append(out.Toolsets, string(ts.Name))
			if ts.Name == tool.Web {
				add(webSessionCheck(ctx, env.Web))
			}
		}
	}

	out.Status = statusOK
	for _, c := range out.Checks {
		out.Status = worst(out.Status, c.Status)
	}
	return tool.OK(out), nil
}

// webSessionCheck probes the web-session client with one tiny read. It runs
// only when the web toolset is enabled.
func webSessionCheck(ctx context.Context, web tool.ASC) check {
	c := check{Name: "web_session"}
	_, err := web.Get(ctx, "/v1/apps", url.Values{"limit": {"1"}, "fields[apps]": {"name"}})
	switch {
	case err == nil:
		c.Status, c.Detail = statusOK, "App Store Connect web session is active (apps_create, privacy_* available)"
	case errors.Is(err, tool.ErrWebSessionRequired):
		c.Status, c.Detail = statusWarn, "no active App Store Connect web session: apps_create and privacy_* will fail"
		c.Fix = "call web_login, or run `app-store-connect-mcp web login` in a terminal"
	default:
		c.Status, c.Detail = statusWarn, "web session check failed: "+tool.SanitizeText(err.Error())
	}
	return c
}

func keyKindCheck(k tool.KeyKind) check {
	switch k {
	case tool.KeyTeam:
		return check{Name: "key_kind", Status: statusOK, Detail: "Team API key (issuer id set)"}
	case tool.KeyIndividual:
		return check{
			Name: "key_kind", Status: statusWarn,
			Detail: "Individual API key (no issuer id): sales, finance and signing tools are unavailable",
			Fix:    "for those tools create a Team API key in App Store Connect > Users and Access > Integrations and set its issuer_id",
		}
	}
	return check{
		Name: "key_kind", Status: statusFail, Detail: "no API key configured",
		Fix: "run `app-store-connect-mcp auth add` or set ASC_KEY_ID, ASC_ISSUER_ID and ASC_PRIVATE_KEY_PATH",
	}
}

// reachabilityChecks makes one cheap authenticated call and reports JWT
// acceptance and Apple reachability from its outcome.
func reachabilityChecks(ctx context.Context, env *tool.Env) []check {
	_, err := env.ASC.Get(ctx, "/v1/apps", url.Values{"limit": {"1"}, "fields[apps]": {"name"}})
	jwt := check{Name: "jwt", Status: statusOK, Detail: "Apple accepted the token"}
	api := check{Name: "apple_api", Status: statusOK, Detail: "GET /v1/apps answered"}
	var ae *tool.APIError
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
		jwt = check{
			Name: "jwt", Status: statusFail, Detail: "Apple rejected the token (401)",
			Fix: "check key_id, issuer_id (omit it only for Individual keys), the .p8 file and the system clock; `app-store-connect-mcp auth test` re-signs",
		}
		api.Detail = "Apple answered 401"
	case errors.As(err, &ae) && ae.Status == http.StatusForbidden:
		api = check{
			Name: "apple_api", Status: statusFail, Detail: "the key lacks access (403): " + ae.Error(),
			Fix: "give the key the App Manager or Admin role (Developer is read-mostly) in Users and Access > Integrations",
		}
	case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
		api = check{
			Name: "apple_api", Status: statusWarn, Detail: "rate limited (429)",
			Fix: "wait for the hourly quota to recover",
		}
	case errors.As(err, &ae):
		api = check{
			Name: "apple_api", Status: statusWarn, Detail: ae.Error(),
			Fix: "Apple may be degraded; see https://developer.apple.com/system-status/",
		}
	default:
		jwt = check{Name: "jwt", Status: statusWarn, Detail: "not verified: the request did not reach Apple"}
		api = check{
			Name: "apple_api", Status: statusFail, Detail: "App Store Connect unreachable: " + err.Error(),
			Fix: "check network, proxy and the key file; `app-store-connect-mcp auth test` shows key errors",
		}
	}
	return []check{jwt, api}
}

func clockCheck(asc tool.ASC) check {
	skew, ok := asc.ClockSkew()
	if !ok {
		return check{Name: "clock_skew", Status: statusWarn, Detail: "not measured (no Date header seen)"}
	}
	abs := skew.Abs().Round(time.Second)
	c := check{Name: "clock_skew", Status: statusOK, Detail: fmt.Sprintf("local clock differs from Apple by %s", abs)}
	switch {
	case abs >= skewFail:
		c.Status = statusFail
	case abs >= skewWarn:
		c.Status = statusWarn
	}
	if c.Status != statusOK {
		c.Fix = "sync the system clock (e.g. enable network time); tokens with a skewed iat/exp are rejected"
	}
	return c
}

func rateCheck(rl tool.RateLimit) check {
	if !rl.Known {
		return check{Name: "rate_limit", Status: statusOK, Detail: "not reported yet"}
	}
	c := check{Name: "rate_limit", Status: statusOK, Detail: fmt.Sprintf("%d of %d calls left this hour", rl.Remaining, rl.Limit)}
	if rl.Remaining < tool.RateLimitWarnBelow {
		c.Status = statusWarn
		c.Fix = "the hourly quota is nearly spent; prefer workflow tools over many asc_api_get calls"
	}
	return c
}

func specCheck(version string) check {
	if version == "" {
		return check{Name: "spec", Status: statusWarn, Detail: "spec index unavailable; asc_api_* tools cannot validate"}
	}
	return check{Name: "spec", Status: statusOK, Detail: "App Store Connect API spec " + version}
}

func writesCheck(allow bool) check {
	if allow {
		return check{Name: "writes", Status: statusOK, Detail: "writes enabled; destructive tools still preview first"}
	}
	return check{
		Name: "writes", Status: statusOK, Detail: "read-only: write tools are hidden",
		Fix: tool.EnableWritesHint,
	}
}

// filesCheck reports the effective root of file tools (export_dir,
// screenshot sync, uploads); a refused root names the fix.
func filesCheck(f tool.Files) (string, check) {
	root, err := f.Resolve(".")
	if err != nil {
		return "", check{
			Name: "files", Status: statusWarn, Detail: "file access refused: " + err.Error(),
			Fix: "start app-store-connect-mcp with --root <project dir> (or set ASC_MCP_ROOT), or open the project folder in a client that sends MCP roots",
		}
	}
	return root, check{Name: "files", Status: statusOK, Detail: "file tools are confined to " + root}
}

func vendorCheck(p tool.ProfileInfo) check {
	if p.VendorNumber != "" {
		return check{Name: "vendor_number", Status: statusOK, Detail: "vendor number set"}
	}
	fix := "set ASC_VENDOR_NUMBER or vendor_number in the profile (App Store Connect > Payments and Financial Reports)"
	if p.Name == "env" {
		fix = "set ASC_VENDOR_NUMBER (App Store Connect > Payments and Financial Reports)"
	}
	return check{
		Name: "vendor_number", Status: statusWarn, Detail: "no vendor number: sales_report and finance_report cannot run",
		Fix: fix,
	}
}

func worst(a, b string) string {
	rank := map[string]int{statusOK: 0, statusWarn: 1, statusFail: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func quoteOr(s, empty string) string {
	if s == "" {
		return empty
	}
	return fmt.Sprintf("%q", s)
}
