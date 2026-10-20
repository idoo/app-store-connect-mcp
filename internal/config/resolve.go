package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Mode environment variables.
const (
	EnvProfile          = "ASC_MCP_PROFILE"
	EnvAllowWrites      = "ASC_MCP_ALLOW_WRITES"
	EnvToolsets         = "ASC_MCP_TOOLSETS"
	EnvDiscovery        = "ASC_MCP_DISCOVERY"
	EnvMaxResponseBytes = "ASC_MCP_MAX_RESPONSE_BYTES"
	EnvMaxRetries       = "ASC_MCP_MAX_RETRIES"
	EnvBaseURL          = "ASC_MCP_BASE_URL"
	EnvCI               = "ASC_MCP_CI"
	// EnvAllowNoElicit lets agents approve human gates in CI; it is
	// honoured only together with EnvCI.
	EnvAllowNoElicit = "ASC_MCP_ALLOW_NO_ELICIT"
)

// ProblemEmptyKey is the profile problem reported when ASC_PRIVATE_KEY is
// set to an empty value, usually a CI secret that isn't available to the job.
const ProblemEmptyKey = EnvKey + " is set but empty (missing CI secret?)"

// Defaults and bounds for limits.
const (
	DefaultMaxResponseBytes = 60000
	MinMaxResponseBytes     = 4096
	MaxMaxResponseBytes     = 10 << 20
	DefaultMaxRetries       = 3
	MaxMaxRetries           = 10
)

// Flags are command-line overrides; nil or empty means "not given".
type Flags struct {
	Profile     string
	AllowWrites *bool
	Toolsets    *string
	Discovery   *bool
	// NoConfirm skips confirmations; honored only when ASC_MCP_CI is true.
	NoConfirm bool
}

// Options are the inputs of Resolve. Zero values use the process environment.
type Options struct {
	Flags Flags
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// LookupEnv distinguishes set-but-empty variables; it defaults to
	// os.LookupEnv when Getenv is nil, otherwise to treating "" as unset.
	LookupEnv func(string) (string, bool)
	// ConfigPath defaults to DefaultPath.
	ConfigPath string
	// Cwd defaults to the working directory; Home to the user's home.
	Cwd, Home string
}

// ResolvedProfile is the effective credentials profile. It holds key
// locations, never key material.
type ResolvedProfile struct {
	Name          string
	KeyID         string
	IssuerID      string
	KeyKind       tool.KeyKind
	Key           KeyRef
	VendorNumber  string
	DefaultApp    string
	AllowWrites   bool
	AllowNoElicit bool
	// PinSource is the .app-store-connect-mcp.toml path when a pin was applied.
	PinSource string
	// Problems are user-fixable issues doctor reports; the server still starts.
	Problems []string
}

// Info maps the profile to the tool contract's ProfileInfo.
func (p ResolvedProfile) Info() tool.ProfileInfo {
	return tool.ProfileInfo{
		Name:          p.Name,
		KeyKind:       p.KeyKind,
		VendorNumber:  p.VendorNumber,
		DefaultApp:    p.DefaultApp,
		AllowWrites:   p.AllowWrites,
		PinSource:     p.PinSource,
		AllowNoElicit: p.AllowNoElicit,
		Problems:      p.Problems,
	}
}

// Resolved is the effective runtime configuration.
type Resolved struct {
	Profile          ResolvedProfile
	Toolsets         []tool.Toolset
	Discovery        bool
	MaxResponseBytes int
	MaxRetries       int
	// BaseURL is a validated loopback override, or "" for Apple's API.
	BaseURL            string
	CI                 bool
	NoConfirm          bool
	AllowProfileSwitch bool
	// Config is the loaded config file, for per-call profile switching.
	Config   *Config
	Warnings []string
	// WritesLocked forbids turning writes on at runtime: a pin sets
	// allow_writes = false, or an untrusted pin chose the profile.
	WritesLocked bool
}

// Resolve loads the config file and project pin and applies precedence:
// flag > env > pin > profile > default. A pin can only narrow writes.
func Resolve(opts Options) (*Resolved, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home, cwd, err := dirs(opts, getenv)
	if err != nil {
		return nil, err
	}
	path := opts.ConfigPath
	if path == "" {
		if path, err = DefaultPath(getenv); err != nil {
			return nil, err
		}
	}
	cfg, warnings, err := Load(path)
	if err != nil {
		return nil, err
	}
	if opts.ConfigPath == "" && !exists(path) {
		warnings = append(warnings, legacyConfigWarnings(path)...)
	}
	pin, err := FindPin(cwd, home)
	if err != nil {
		return nil, err
	}
	r := &Resolved{Config: cfg, AllowProfileSwitch: cfg.AllowProfileSwitch, Warnings: warnings}
	if err := r.resolveProfile(opts.Flags, getenv, cfg, pin); err != nil {
		return nil, err
	}
	r.emptyKeyProblem(opts, getenv)
	r.keyPermIssues()
	if err := r.resolveModes(opts.Flags, getenv); err != nil {
		return nil, err
	}
	if err := r.resolveNoElicit(getenv); err != nil {
		return nil, err
	}
	return r, nil
}

// keyPermIssues reports the resolved key file's permissions the way Load
// reports config.toml's: readable by others is a warning; writable by
// others is a problem (Read refuses the key, so signing fails).
func (r *Resolved) keyPermIssues() {
	w, err := r.Profile.Key.CheckPerm()
	if err != nil {
		r.Profile.Problems = append(r.Profile.Problems, err.Error())
		return
	}
	r.Warnings = append(r.Warnings, w...)
}

// emptyKeyProblem flags an env profile whose inline key variable is present
// but blank, which env credentials otherwise treat as unset.
func (r *Resolved) emptyKeyProblem(opts Options, getenv func(string) string) {
	if r.Profile.Name != EnvProfileName || r.Profile.Key.Source != "" {
		return
	}
	lookup := opts.LookupEnv
	if lookup == nil {
		if opts.Getenv == nil {
			lookup = os.LookupEnv
		} else {
			lookup = func(k string) (string, bool) { v := getenv(k); return v, v != "" }
		}
	}
	if v, set := lookup(EnvKey); set && strings.TrimSpace(v) == "" {
		r.Profile.Problems = append(r.Profile.Problems, ProblemEmptyKey)
	}
}

// resolveNoElicit applies ASC_MCP_ALLOW_NO_ELICIT, which takes effect only
// with ASC_MCP_CI; a pin never widens it.
func (r *Resolved) resolveNoElicit(getenv func(string) string) error {
	v, set, err := envBool(getenv, EnvAllowNoElicit)
	if err != nil || !set || !v {
		return err
	}
	if !r.CI {
		r.Profile.Problems = append(r.Profile.Problems, EnvAllowNoElicit+" ignored: it takes effect only with "+EnvCI+"=1")
		return nil
	}
	r.Profile.AllowNoElicit = true
	return nil
}

func dirs(opts Options, getenv func(string) string) (home, cwd string, err error) {
	home, cwd = opts.Home, opts.Cwd
	if home == "" {
		if home = getenv("HOME"); home == "" {
			if home, err = os.UserHomeDir(); err != nil {
				return "", "", fmt.Errorf("resolve: home: %w", err)
			}
		}
	}
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return "", "", fmt.Errorf("resolve: cwd: %w", err)
		}
	}
	return home, cwd, nil
}

// legacyConfigDirs are config directory names of earlier releases, siblings
// of the current one.
var legacyConfigDirs = []string{"asc-mcp", "hamn"}

// legacyConfigWarnings names an earlier release's config file next to the
// missing default at path when it holds at least one profile with a key_id.
// Files in another schema (other servers use asc-mcp names) are ignored.
func legacyConfigWarnings(path string) []string {
	newDir := filepath.Dir(path)
	var out []string
	for _, name := range legacyConfigDirs {
		old := filepath.Join(filepath.Dir(newDir), name, "config.toml")
		data, err := os.ReadFile(old) //nolint:gosec // fixed sibling of the user's own config location
		if err != nil {
			continue
		}
		cfg, err := Parse(data)
		if err != nil || !hasKeyedProfile(cfg) {
			continue
		}
		out = append(out, fmt.Sprintf(`config %s is no longer read; move it: mkdir -p "%s" && mv "%s" "%s"`, old, newDir, old, path))
	}
	return out
}

func hasKeyedProfile(cfg *Config) bool {
	for _, p := range cfg.Profiles {
		if p.KeyID != "" {
			return true
		}
	}
	return false
}

func (r *Resolved) resolveProfile(f Flags, getenv func(string) string, cfg *Config, pin *Pin) error {
	env, hasEnv, err := envCredentials(getenv)
	if err != nil {
		return err
	}
	r.Warnings = append(r.Warnings, env.Warnings...)
	trusted := pin != nil && slices.ContainsFunc(cfg.TrustedPins, func(d string) bool { return sameDir(d, pin.Dir()) })

	name, pinSelected := "", false
	switch {
	case f.Profile != "":
		name = f.Profile
	case getenv(EnvProfile) != "":
		name = strings.TrimSpace(getenv(EnvProfile))
	case hasEnv:
		name = EnvProfileName
	case pin != nil && pin.Profile != "":
		target, ok := cfg.Profiles[pin.Profile]
		switch {
		case !ok:
			return fmt.Errorf("pin %s: profile %q is not defined in the config", pin.Path, pin.Profile)
		case trusted || !target.AllowWrites:
			name, pinSelected = pin.Profile, true
		default:
			r.Warnings = append(r.Warnings, fmt.Sprintf("ignoring profile %q from untrusted pin %s: it allows writes; add the directory to trusted_pins to allow this", pin.Profile, pin.Path))
		}
	}
	if err := r.loadProfile(name, cfg, env, hasEnv, getenv); err != nil {
		return err
	}
	if err := r.resolveWrites(f, getenv, pin); err != nil {
		return err
	}
	if pinSelected && !trusted {
		if r.Profile.AllowWrites {
			r.Warnings = append(r.Warnings, fmt.Sprintf("ignoring profile %q from untrusted pin %s: writes are on (%s or --allow-writes); add the directory to trusted_pins to allow this", pin.Profile, pin.Path, EnvAllowWrites))
			pinSelected = false
			r.Profile = ResolvedProfile{}
			if err := r.loadProfile("", cfg, env, hasEnv, getenv); err != nil {
				return err
			}
			if err := r.resolveWrites(f, getenv, pin); err != nil {
				return err
			}
		} else {
			r.WritesLocked = true
			r.Warnings = append(r.Warnings, fmt.Sprintf("writes locked: profile %q was chosen by untrusted pin %s", pin.Profile, pin.Path))
		}
	}
	if pinSelected {
		r.Profile.PinSource = pin.Path
	}
	r.applyPin(pin, trusted)
	return nil
}

// loadProfile sets r.Profile to the named profile; "" selects the default
// profile, or the only one.
func (r *Resolved) loadProfile(name string, cfg *Config, env envCreds, hasEnv bool, getenv func(string) string) error {
	if name == "" {
		name = cfg.DefaultProfile
	}
	if name == "" && len(cfg.Profiles) == 1 {
		name = cfg.ProfileNames()[0]
	}

	p := &r.Profile
	switch name {
	case "":
		p.Problems = append(p.Problems, "no credentials: set "+EnvKeyID+", "+EnvIssuerID+" and "+EnvKeyPath+", or run `app-store-connect-mcp auth import`")
	case EnvProfileName:
		if !hasEnv {
			return fmt.Errorf("profile %q selected but %s is not set", EnvProfileName, EnvKeyID)
		}
		if s := cfg.Env; s != nil {
			fill(&env.Profile.VendorNumber, s.VendorNumber)
			fill(&env.Profile.DefaultApp, s.DefaultApp)
		}
		r.setProfile(EnvProfileName, env.Profile, env.Key)
		p.Problems = append(p.Problems, env.Problems...)
	default:
		prof, ok := cfg.Profiles[name]
		if !ok {
			return fmt.Errorf("profile %q is not defined (known: %s)", name, strings.Join(cfg.ProfileNames(), ", "))
		}
		key := KeyRef{Source: KeyFromFile, Path: ExpandHome(prof.KeyPath, getenv), KeyID: prof.KeyID}
		if prof.KeySource == KeySourceKeychain {
			key = KeyRef{Source: KeyFromKeychain, KeyID: prof.KeyID}
		}
		r.setProfile(name, prof, key)
		p.Problems = append(p.Problems, kindProblems(prof, "issuer_id in profile "+strconv.Quote(name))...)
	}
	return nil
}

func (r *Resolved) setProfile(name string, prof Profile, key KeyRef) {
	kind, _ := prof.ResolvedKind()
	r.Profile = ResolvedProfile{
		Name:          name,
		KeyID:         prof.KeyID,
		IssuerID:      prof.IssuerID,
		KeyKind:       kind,
		Key:           key,
		VendorNumber:  prof.VendorNumber,
		DefaultApp:    prof.DefaultApp,
		AllowWrites:   prof.AllowWrites,
		AllowNoElicit: prof.AllowNoElicit,
	}
}

// applyPin sets the pin's default app. An untrusted pin may set it only when
// the effective profile is read-only.
func (r *Resolved) applyPin(pin *Pin, trusted bool) {
	if pin == nil || pin.App == "" {
		return
	}
	if !trusted && r.Profile.AllowWrites {
		r.Warnings = append(r.Warnings, fmt.Sprintf("ignoring app %q from untrusted pin %s: profile %q allows writes", pin.App, pin.Path, r.Profile.Name))
		return
	}
	r.Profile.DefaultApp = pin.App
	r.Profile.PinSource = pin.Path
}

// resolveWrites applies flag > env > profile, then lets the pin narrow.
func (r *Resolved) resolveWrites(f Flags, getenv func(string) string, pin *Pin) error {
	allow := r.Profile.AllowWrites
	if v, ok, err := envBool(getenv, EnvAllowWrites); err != nil {
		return err
	} else if ok {
		allow = v
	}
	if f.AllowWrites != nil {
		allow = *f.AllowWrites
	}
	if pin != nil && pin.AllowWrites != nil {
		if *pin.AllowWrites {
			r.Warnings = append(r.Warnings, fmt.Sprintf("ignoring allow_writes = true in pin %s: a pin can only disable writes", pin.Path))
		} else {
			if allow {
				r.Warnings = append(r.Warnings, fmt.Sprintf("writes disabled by pin %s", pin.Path))
			}
			allow = false
			r.WritesLocked = true
			r.Profile.PinSource = pin.Path
		}
	}
	r.Profile.AllowWrites = allow && r.Profile.Name != ""
	return nil
}

func (r *Resolved) resolveModes(f Flags, getenv func(string) string) error {
	expr := getenv(EnvToolsets)
	if f.Toolsets != nil {
		expr = *f.Toolsets
	}
	ts, err := ParseToolsets(expr)
	if err != nil {
		return err
	}
	r.Toolsets = ts

	discovery, _, err := envBool(getenv, EnvDiscovery)
	if err != nil {
		return err
	}
	if f.Discovery != nil {
		discovery = *f.Discovery
	}
	r.Discovery = discovery

	if r.MaxResponseBytes, err = envInt(getenv, EnvMaxResponseBytes, DefaultMaxResponseBytes, MinMaxResponseBytes, MaxMaxResponseBytes); err != nil {
		return err
	}
	if r.MaxRetries, err = envInt(getenv, EnvMaxRetries, DefaultMaxRetries, 0, MaxMaxRetries); err != nil {
		return err
	}
	if raw := getenv(EnvBaseURL); raw != "" {
		if r.BaseURL, err = ValidateBaseURL(raw); err != nil {
			return err
		}
	}
	if r.CI, _, err = envBool(getenv, EnvCI); err != nil {
		return err
	}
	r.NoConfirm = f.NoConfirm && r.CI
	if f.NoConfirm && !r.CI {
		r.Warnings = append(r.Warnings, "--no-confirm ignored: it takes effect only with "+EnvCI+"=1")
	}
	return nil
}

func envBool(getenv func(string) string, name string) (value, set bool, err error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return false, false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false, fmt.Errorf("%s=%q: want true/false/1/0", name, raw)
	}
	return v, true, nil
}

func envInt(getenv func(string) string, name string, def, lo, hi int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%s=%q: want an integer in [%d, %d]", name, raw, lo, hi)
	}
	return v, nil
}
