package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/app"
	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/auth"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/tool"
)

const authUsage = `usage:
  hamn auth add                    interactive: add a profile
  hamn auth import                 import fastlane / env / AuthKey_*.p8 credentials
  hamn auth list                   list profiles
  hamn auth test [PROFILE]         sign a JWT and call GET /v1/apps?limit=1
  hamn auth remove NAME            remove a profile
  hamn auth set NAME key=value...  set vendor_number, default_app, allow_writes, issuer_id, key_kind`

func runAuth(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(authUsage)
	}
	p := &prompter{in: bufio.NewReader(stdin), out: stderr}
	switch args[0] {
	case "add":
		return authAdd(p, stdout, stderr)
	case "import":
		return authImport(p, stdout, stderr)
	case "list":
		return authList(stdout, stderr)
	case "test":
		if len(args) > 2 {
			return usageErr(authUsage)
		}
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		return authTest(ctx, name, stdout)
	case "remove":
		if len(args) != 2 {
			return usageErr(authUsage)
		}
		return authRemove(args[1], stdout, stderr)
	case "set":
		if len(args) < 3 {
			return usageErr(authUsage)
		}
		return authSet(args[1], args[2:], stdout, stderr)
	default:
		return usageErr(fmt.Sprintf("unknown auth subcommand %q\n%s", args[0], authUsage))
	}
}

// prompter asks questions on stderr and reads answers from stdin.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) ask(q, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", q)
	}
	line, err := p.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("read answer: %w", err)
	}
	if s := strings.TrimSpace(line); s != "" {
		return s, nil
	}
	return def, nil
}

func (p *prompter) yes(q string) (bool, error) {
	a, err := p.ask(q+" (y/N)", "")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"), nil
}

// loadConfig loads the user's config file, printing warnings to stderr.
func loadConfig(stderr io.Writer) (string, *config.Config, error) {
	path, err := config.DefaultPath(os.Getenv)
	if err != nil {
		return "", nil, fmt.Errorf("config path: %w", err)
	}
	cfg, warnings, err := config.Load(path)
	if err != nil {
		return "", nil, fmt.Errorf("load %s: %w", path, err)
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	return path, cfg, nil
}

func saveProfile(path string, cfg *config.Config, name string, prof config.Profile, stdout io.Writer) error {
	cfg.Profiles[name] = prof
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = name
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "saved profile %q to %s\n", name, path)
	return nil
}

// askKeyStorage sets KeyPath or moves the key into the keychain.
func askKeyStorage(p *prompter, prof *config.Profile, keyPath string) error {
	keychain, err := p.yes("Store the key in the OS keychain instead of referencing the file?")
	if err != nil {
		return err
	}
	if !keychain {
		prof.KeyPath = keyPath
		return nil
	}
	data, err := os.ReadFile(keyPath) //nolint:gosec // path chosen by the user.
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}
	return storeInKeychain(prof, data)
}

func storeInKeychain(prof *config.Profile, pem []byte) error {
	if err := auth.StoreKey(prof.KeyID, pem); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	prof.KeySource = config.KeySourceKeychain
	prof.KeyPath = ""
	return nil
}

// askCommon fills the issuer, vendor number, default app and writes answers.
func askCommon(p *prompter, prof *config.Profile) error {
	var err error
	if prof.IssuerID == "" && prof.KeyKind == "" {
		if prof.IssuerID, err = p.ask("Issuer ID (empty for an Individual API key)", ""); err != nil {
			return err
		}
	}
	prof.KeyKind = tool.KeyTeam
	if prof.IssuerID == "" {
		prof.KeyKind = tool.KeyIndividual
	}
	if prof.VendorNumber, err = p.ask("Vendor number (sales/finance reports; optional)", prof.VendorNumber); err != nil {
		return err
	}
	if prof.DefaultApp, err = p.ask("Default app id or bundle id (optional)", prof.DefaultApp); err != nil {
		return err
	}
	prof.AllowWrites, err = p.yes("Allow writes by default for this profile?")
	return err
}

func authAdd(p *prompter, stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	name, err := p.ask("Profile name", "default")
	if err != nil {
		return err
	}
	if _, exists := cfg.Profiles[name]; exists {
		if ok, err := p.yes(fmt.Sprintf("Profile %q exists. Replace it?", name)); err != nil || !ok {
			return errors.Join(err, errors.New("aborted"))
		}
	}
	var prof config.Profile
	if prof.KeyID, err = p.ask("Key ID", ""); err != nil {
		return err
	}
	if prof.KeyID == "" {
		return errors.New("key id is required")
	}
	keyPath, err := p.ask("Path to the .p8 private key", "")
	if err != nil {
		return err
	}
	if keyPath == "" {
		return errors.New("key path is required")
	}
	if keyPath, err = absPath(keyPath); err != nil {
		return err
	}
	if _, err := auth.ResolveKey(context.Background(), prof.KeyID, auth.FileSource{Path: keyPath}); err != nil {
		return fmt.Errorf("check key: %w", err)
	}
	if err := askKeyStorage(p, &prof, keyPath); err != nil {
		return err
	}
	if err := askCommon(p, &prof); err != nil {
		return err
	}
	return saveProfile(path, cfg, name, prof, stdout)
}

func absPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		p = home + p[1:]
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("key path: %w", err)
	}
	return abs, nil
}

func authImport(p *prompter, stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cwd: %w", err)
	}
	cands := config.Discover(home, cwd, os.Getenv)
	if len(cands) == 0 {
		return errors.New("no credentials found (fastlane/api_key.json, ASC_* env, AuthKey_*.p8); use `hamn auth add`")
	}
	for i, c := range cands {
		fmt.Fprintf(stderr, "%d) key %s from %s", i+1, c.Profile.KeyID, strings.Join(c.Sources, ", "))
		if len(c.Missing) > 0 {
			fmt.Fprintf(stderr, " (missing: %s)", strings.Join(c.Missing, ", "))
		}
		fmt.Fprintln(stderr)
	}
	pick, err := p.ask("Import which", "1")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(pick)
	if err != nil || n < 1 || n > len(cands) {
		return fmt.Errorf("invalid choice %q", pick)
	}
	c := cands[n-1]
	prof := c.Profile
	name, err := p.ask("Profile name", "default")
	if err != nil {
		return err
	}
	switch c.Key.Source {
	case config.KeyFromFile:
		if err := askKeyStorage(p, &prof, c.Key.Path); err != nil {
			return err
		}
	case config.KeyFromKeychain:
		prof.KeySource = config.KeySourceKeychain
	case config.KeyFromEnv, config.KeyFromFastlaneJSON:
		// Config never holds key material, so inline keys must move to the keychain.
		ok, err := p.yes(fmt.Sprintf("The key is inline (%s). Store it in the OS keychain?", c.Key))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted: an inline key must be stored in the keychain or saved as a .p8 file")
		}
		data, err := c.Key.Read(os.Getenv)
		if err != nil {
			return fmt.Errorf("read key: %w", err)
		}
		if err := storeInKeychain(&prof, data); err != nil {
			return err
		}
	default:
		keyPath, err := p.ask("Path to the .p8 private key", "")
		if err != nil {
			return err
		}
		if keyPath, err = absPath(keyPath); err != nil {
			return err
		}
		if err := askKeyStorage(p, &prof, keyPath); err != nil {
			return err
		}
	}
	if err := askCommon(p, &prof); err != nil {
		return err
	}
	return saveProfile(path, cfg, name, prof, stdout)
}

func authList(stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	if len(cfg.Profiles) == 0 {
		fmt.Fprintf(stdout, "no profiles in %s (run `hamn auth add`)\n", path)
		return nil
	}
	for _, name := range cfg.ProfileNames() {
		pr := cfg.Profiles[name]
		mark := " "
		if name == cfg.DefaultProfile {
			mark = "*"
		}
		kind, _ := pr.ResolvedKind()
		key := pr.KeyPath
		if pr.KeySource != "" {
			key = pr.KeySource
		}
		fmt.Fprintf(stdout, "%s %-16s key=%s kind=%s key_source=%s writes=%t", mark, name, pr.KeyID, kind, key, pr.AllowWrites)
		if pr.VendorNumber != "" {
			fmt.Fprintf(stdout, " vendor=%s", pr.VendorNumber)
		}
		if pr.DefaultApp != "" {
			fmt.Fprintf(stdout, " app=%s", pr.DefaultApp)
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

// probeApple calls GET /v1/apps?limit=1 with the resolved profile.
func probeApple(ctx context.Context, r *config.Resolved) (*asc.Client, *tool.Response, error) {
	c, err := app.NewClient(ctx, r, buildVersion(), nil)
	if err != nil {
		return nil, nil, err
	}
	c.SetAllowWrites(false) // the probe only reads
	resp, err := c.Get(ctx, "/v1/apps", url.Values{"limit": {"1"}})
	if err != nil {
		return c, nil, fmt.Errorf("GET /v1/apps: %w", err)
	}
	return c, resp, nil
}

func authTest(ctx context.Context, name string, stdout io.Writer) error {
	r, err := config.Resolve(config.Options{Flags: config.Flags{Profile: name}})
	if err != nil {
		return err
	}
	c, _, err := probeApple(ctx, r)
	if err != nil {
		return fmt.Errorf("profile %q: %w", r.Profile.Name, err)
	}
	fmt.Fprintf(stdout, "OK profile %q (key %s, %s)", r.Profile.Name, r.Profile.KeyID, r.Profile.KeyKind)
	if rl := c.RateLimit(); rl.Known {
		fmt.Fprintf(stdout, ", rate limit %d/%d remaining this hour", rl.Remaining, rl.Limit)
	}
	fmt.Fprintln(stdout)
	return nil
}

func authRemove(name string, stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	prof, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("no profile %q", name)
	}
	delete(cfg.Profiles, name)
	if cfg.DefaultProfile == name {
		cfg.DefaultProfile = ""
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "removed profile %q\n", name)
	if prof.KeySource == config.KeySourceKeychain && !keyStillUsed(cfg, prof.KeyID) {
		if err := auth.DeleteKey(prof.KeyID); err != nil && !errors.Is(err, auth.ErrKeyNotInKeychain) {
			return fmt.Errorf("delete keychain key: %w", err)
		}
		fmt.Fprintf(stdout, "deleted key %s from the keychain\n", prof.KeyID)
	}
	return nil
}

func keyStillUsed(cfg *config.Config, keyID string) bool {
	for _, p := range cfg.Profiles {
		if p.KeyID == keyID && p.KeySource == config.KeySourceKeychain {
			return true
		}
	}
	return false
}

func authSet(name string, pairs []string, stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	prof, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("no profile %q", name)
	}
	for _, kv := range pairs {
		if err := setField(&prof, kv); err != nil {
			return err
		}
	}
	cfg.Profiles[name] = prof
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "updated profile %q\n", name)
	return nil
}

// setField applies one key=value assignment to prof.
func setField(prof *config.Profile, kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return usageErr(fmt.Sprintf("want key=value, got %q", kv))
	}
	switch k {
	case "vendor_number":
		prof.VendorNumber = v
	case "default_app":
		prof.DefaultApp = v
	case "issuer_id":
		prof.IssuerID = v
	case "allow_writes":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return usageErr(fmt.Sprintf("allow_writes: %v", err))
		}
		prof.AllowWrites = b
	case "key_kind":
		kind := tool.KeyKind(v)
		if kind != tool.KeyTeam && kind != tool.KeyIndividual {
			return usageErr("key_kind must be team or individual")
		}
		prof.KeyKind = kind
	default:
		return usageErr(fmt.Sprintf("unknown key %q (vendor_number, default_app, allow_writes, issuer_id, key_kind)", k))
	}
	return nil
}
