package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// KeySourceKeychain is the only accepted non-empty value of Profile.KeySource.
const KeySourceKeychain = "keychain"

// Config is the user's config file (~/.config/asc-mcp/config.toml).
type Config struct {
	// DefaultProfile names the profile used when nothing else selects one.
	DefaultProfile string `toml:"default_profile,omitempty"`
	// TrustedPins lists absolute directories whose .asc-mcp.toml may select any profile.
	TrustedPins []string `toml:"trusted_pins,omitempty"`
	// AllowProfileSwitch exposes a per-call profile argument on tools.
	AllowProfileSwitch bool `toml:"allow_profile_switch,omitempty"`
	// Profiles maps profile names to credentials references.
	Profiles map[string]Profile `toml:"profiles,omitempty"`
	// Env holds settings for the "env" profile built from ASC_* variables,
	// used when the matching variable is unset (`asc-mcp auth set env ...`).
	Env *EnvSettings `toml:"env,omitempty"`
}

// EnvSettings are the non-secret settings of the "env" profile.
type EnvSettings struct {
	VendorNumber string `toml:"vendor_number,omitempty"`
	DefaultApp   string `toml:"default_app,omitempty"`
}

// Profile is one named set of App Store Connect credentials references.
// It never holds key material, only where to find it.
type Profile struct {
	KeyID    string       `toml:"key_id"`
	IssuerID string       `toml:"issuer_id,omitempty"`
	KeyKind  tool.KeyKind `toml:"key_kind,omitempty"`
	// KeyPath is the path to the .p8 file; mutually exclusive with KeySource.
	KeyPath string `toml:"key_path,omitempty"`
	// KeySource is "keychain" when the key lives in the OS keychain.
	KeySource     string `toml:"key_source,omitempty"`
	VendorNumber  string `toml:"vendor_number,omitempty"`
	DefaultApp    string `toml:"default_app,omitempty"`
	AllowWrites   bool   `toml:"allow_writes,omitempty"`
	AllowNoElicit bool   `toml:"allow_no_elicit,omitempty"`
}

// Validate reports structural errors that make the profile unusable.
func (p Profile) Validate() error {
	var errs []error
	if p.KeyID == "" {
		errs = append(errs, errors.New("key_id is required"))
	}
	switch p.KeyKind {
	case "", tool.KeyTeam, tool.KeyIndividual:
	default:
		errs = append(errs, fmt.Errorf("key_kind %q must be %q or %q", p.KeyKind, tool.KeyTeam, tool.KeyIndividual))
	}
	if p.KeyKind == tool.KeyIndividual && p.IssuerID != "" {
		errs = append(errs, errors.New("issuer_id must be empty for an individual key"))
	}
	switch {
	case p.KeySource != "" && p.KeySource != KeySourceKeychain:
		errs = append(errs, fmt.Errorf("key_source %q must be %q", p.KeySource, KeySourceKeychain))
	case p.KeySource != "" && p.KeyPath != "":
		errs = append(errs, errors.New("set key_path or key_source, not both"))
	case p.KeySource == "" && p.KeyPath == "":
		errs = append(errs, errors.New("key_path or key_source=\"keychain\" is required"))
	}
	return errors.Join(errs...)
}

// ResolvedKind returns the explicit key kind, or the kind implied by the
// issuer id: team when present, otherwise individual, matching the signer,
// which omits iss without an issuer. The second result is false when the kind
// is inferred from a missing issuer rather than declared.
func (p Profile) ResolvedKind() (tool.KeyKind, bool) {
	switch {
	case p.KeyKind != "":
		return p.KeyKind, true
	case p.IssuerID != "":
		return tool.KeyTeam, true
	default:
		return tool.KeyIndividual, false
	}
}

// DefaultPath returns the config file path: $XDG_CONFIG_HOME/asc-mcp/config.toml,
// ~/.config/asc-mcp/config.toml, or %AppData%\asc-mcp\config.toml on Windows.
func DefaultPath(getenv func(string) string) (string, error) {
	if runtime.GOOS == "windows" {
		if d := getenv("AppData"); d != "" {
			return filepath.Join(d, "asc-mcp", "config.toml"), nil
		}
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "asc-mcp", "config.toml"), nil
	}
	home := getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config path: %w", err)
		}
		home = h
	}
	return filepath.Join(home, ".config", "asc-mcp", "config.toml"), nil
}

// Load reads the config at path. A missing file yields an empty Config.
// Group- or world-writable files are refused; files readable by others load
// with a warning. Unknown keys are rejected so typos never silently drop
// settings.
func Load(path string) (*Config, []string, error) {
	data, info, err := readChecked(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := checkDir(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}
	warnings := permWarnings(path, info)
	cfg, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, warnings, nil
}

// Parse decodes and validates config TOML.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := decodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeStrict decodes TOML rejecting unknown keys, naming them in the error.
func decodeStrict(data []byte, v any) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		keys := make([]string, 0, len(strict.Errors))
		for _, e := range strict.Errors {
			keys = append(keys, strings.Join(e.Key(), "."))
		}
		return fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Errorf("line %d, column %d: %s", row, col, de.Error())
	}
	return err
}

// Validate checks every profile and cross-references.
func (c *Config) Validate() error {
	var errs []error
	for _, name := range c.ProfileNames() {
		if name == EnvProfileName {
			errs = append(errs, fmt.Errorf("profile name %q is reserved for environment credentials", EnvProfileName))
			continue
		}
		if err := c.Profiles[name].Validate(); err != nil {
			errs = append(errs, fmt.Errorf("profile %q: %w", name, err))
		}
	}
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			errs = append(errs, fmt.Errorf("default_profile %q is not defined", c.DefaultProfile))
		}
	}
	for _, p := range c.TrustedPins {
		if !filepath.IsAbs(p) {
			errs = append(errs, fmt.Errorf("trusted_pins entry %q must be an absolute path", p))
		}
	}
	return errors.Join(errs...)
}

// ProfileNames returns the profile names in sorted order.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Save validates cfg and writes it atomically to path with mode 0600,
// creating the parent directory with mode 0700.
func Save(path string, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("save config: encode: %w", err)
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	f, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("save config: chmod: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("save config: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("save config: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save config: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("save config: rename: %w", err)
	}
	ok = true
	return nil
}

// readChecked reads a config-like file after refusing unsafe permissions.
func readChecked(path string) ([]byte, fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if err := checkWritable(path, info); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is the user's own config location
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, info, nil
}

// checkWritable refuses files that other users can modify (unix only).
func checkWritable(path string, info fs.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("refusing %s: writable by group or others (mode %04o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

// checkDir refuses a config directory other users can write to, since they
// could replace config.toml (unix only; sticky directories are exempt).
func checkDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&fs.ModeSticky == 0 {
		return fmt.Errorf("refusing %s: writable by group or others (mode %04o); run chmod 700 %s", dir, info.Mode().Perm(), dir)
	}
	return nil
}

func permWarnings(path string, info fs.FileInfo) []string {
	if runtime.GOOS == "windows" || info.Mode().Perm()&0o044 == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s is readable by other users (mode %04o); run chmod 600 %s", path, info.Mode().Perm(), path)}
}
