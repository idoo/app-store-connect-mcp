package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/app"
	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/auth"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/tool"
)

const authUsage = `usage:
  hamn auth add [--kind team|individual] [--no-verify]     interactive: add a profile
  hamn auth import [--kind team|individual] [--no-verify]  import fastlane / ASC_* env / AuthKey_*.p8 credentials
  hamn auth list                    list profiles
  hamn auth test [PROFILE]          sign a JWT and call GET /v1/apps?limit=1
  hamn auth remove NAME             remove a profile
  hamn auth set [NAME] key=value... set vendor_number, default_app, allow_writes, issuer_id, key_kind
                                    (NAME defaults to env when ASC_KEY_ID is set, else the default profile;
                                    "env" stores vendor_number/default_app for ASC_* users)`

// issuerHelp says where Apple shows the issuer id.
const issuerHelp = "App Store Connect → Users and Access → Integrations → App Store Connect API, shown above the key list"

// addOptions are the flags of `auth add` and `auth import`.
type addOptions struct {
	kind     string
	noVerify bool
}

func parseAddFlags(name string, args []string, stderr io.Writer) (addOptions, error) {
	var o addOptions
	fs := flag.NewFlagSet("auth "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.kind, "kind", "", "key type: team or individual (asked when omitted)")
	fs.BoolVar(&o.noVerify, "no-verify", false, "skip the test call to Apple after saving")
	if err := fs.Parse(args); err != nil {
		return o, usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return o, usageErr(authUsage)
	}
	switch tool.KeyKind(o.kind) {
	case "", tool.KeyTeam, tool.KeyIndividual:
	default:
		return o, usageErr("--kind must be team or individual")
	}
	return o, nil
}

func runAuth(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(authUsage)
	}
	if wantsHelp(args) {
		_, err := fmt.Fprintln(stdout, authUsage)
		return err
	}
	p := &prompter{in: bufio.NewReader(stdin), out: stderr}
	switch args[0] {
	case "add", "import":
		o, err := parseAddFlags(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		var name string
		if args[0] == "add" {
			name, err = authAdd(p, o, stdout, stderr)
		} else {
			name, err = authImport(p, o, stdout, stderr)
		}
		if err != nil {
			return err
		}
		return afterSave(ctx, name, o.noVerify, stdout)
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
		rest := args[1:]
		name := ""
		if len(rest) > 0 && !strings.Contains(rest[0], "=") {
			name, rest = rest[0], rest[1:]
		}
		if len(rest) == 0 {
			return usageErr(authUsage)
		}
		return authSet(name, rest, stdout, stderr)
	default:
		return usageErr(fmt.Sprintf("unknown auth subcommand %q\n%s", args[0], authUsage))
	}
}

// wantsHelp reports whether args ask for help; such calls never run the command.
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			return true
		case "--":
			return false
		}
	}
	return false
}

// afterSave checks the new profile against Apple (unless noVerify) and
// prints the next step.
func afterSave(ctx context.Context, name string, noVerify bool, stdout io.Writer) error {
	r, err := config.Resolve(config.Options{Flags: config.Flags{Profile: name}})
	if err != nil {
		return err
	}
	if !noVerify {
		if _, _, err := probeApple(ctx, r); err != nil {
			return fmt.Errorf("profile %q is saved, but the check against Apple failed: %w%s", name, err, authChecklist(err, r.Profile))
		}
		fmt.Fprintln(stdout, "Apple accepted the key (GET /v1/apps)")
	}
	if r.Profile.VendorNumber == "" && r.Profile.KeyKind == tool.KeyTeam {
		fmt.Fprintf(stdout, "for sales and finance reports: hamn auth set %s vendor_number=<number> (Payments and Financial Reports, top left)\n", name)
	}
	fmt.Fprintf(stdout, "next: hamn config install <%s>\n", strings.Join(clients, "|"))
	return nil
}

// authChecklist explains a 401 from Apple: the three inputs that can be wrong.
func authChecklist(err error, p config.ResolvedProfile) string {
	var ae *tool.APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nApple rejected the token. Check:")
	b.WriteString("\n  - the key id matches the AuthKey_<ID>.p8 file name")
	b.WriteString("\n  - the key hasn't been revoked (Users and Access → Integrations)")
	if p.KeyKind == tool.KeyIndividual {
		fmt.Fprintf(&b, "\n  - this is really an Individual key. If it is a team key: hamn auth set %s key_kind=team issuer_id=<issuer>", p.Name)
	} else {
		b.WriteString("\n  - the issuer id is the one from " + issuerHelp)
	}
	return b.String()
}

var authKeyFile = regexp.MustCompile(`^AuthKey_([A-Za-z0-9]+)\.p8$`)

// keyIDMismatch returns the id in an AuthKey_<ID>.p8 file name when it
// differs from keyID.
func keyIDMismatch(keyID, path string) (string, bool) {
	m := authKeyFile.FindStringSubmatch(filepath.Base(path))
	if m == nil || strings.EqualFold(m[1], keyID) {
		return "", false
	}
	return m[1], true
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
		return "", nil, err
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

// askKind settles team vs individual explicitly (flag, saved kind, issuer,
// else a question defaulting to team) and requires the issuer for team keys.
// Pressing Enter never turns a team key into an individual one.
func askKind(p *prompter, prof *config.Profile, flagKind string, stderr io.Writer) error {
	kind := tool.KeyKind(flagKind)
	if kind == "" {
		kind = prof.KeyKind
	}
	if kind == "" && prof.IssuerID != "" {
		kind = tool.KeyTeam
	}
	if kind == "" {
		a, err := p.ask("Key type: [t]eam or [i]ndividual (Integrations shows team keys; Individual keys live under your user profile)", "team")
		if err != nil {
			return err
		}
		switch strings.ToLower(a) {
		case "t", "team":
			kind = tool.KeyTeam
		case "i", "individual":
			kind = tool.KeyIndividual
		default:
			return fmt.Errorf("key type %q: answer team or individual", a)
		}
	}
	prof.KeyKind = kind
	if kind == tool.KeyIndividual {
		if prof.IssuerID != "" {
			fmt.Fprintln(stderr, "note: Individual keys have no issuer; dropping the issuer id")
			prof.IssuerID = ""
		}
		return nil
	}
	if prof.IssuerID == "" {
		var err error
		if prof.IssuerID, err = p.ask("Issuer ID ("+issuerHelp+")", ""); err != nil {
			return err
		}
		if prof.IssuerID == "" {
			return errors.New("a team key needs its issuer id (" + issuerHelp + "); rerun with --kind individual if this is an Individual key")
		}
	}
	if !uuidRe.MatchString(prof.IssuerID) {
		fmt.Fprintf(stderr, "warning: issuer id %q doesn't look like Apple's (a UUID such as 57246542-96fe-1a63-e053-0824d011072a)\n", prof.IssuerID)
	}
	return nil
}

// askCommon fills the key kind and issuer, vendor number, default app and writes answers.
func askCommon(p *prompter, prof *config.Profile, o addOptions, stderr io.Writer) error {
	if err := askKind(p, prof, o.kind, stderr); err != nil {
		return err
	}
	var err error
	if prof.VendorNumber, err = p.ask("Vendor number (sales/finance reports; optional)", prof.VendorNumber); err != nil {
		return err
	}
	if prof.DefaultApp, err = p.ask("Default app id or bundle id (optional)", prof.DefaultApp); err != nil {
		return err
	}
	prof.AllowWrites, err = p.yes("Allow writes by default for this profile?")
	return err
}

func authAdd(p *prompter, o addOptions, stdout, stderr io.Writer) (string, error) {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return "", err
	}
	name, err := p.ask("Profile name", "default")
	if err != nil {
		return "", err
	}
	if _, exists := cfg.Profiles[name]; exists {
		if ok, err := p.yes(fmt.Sprintf("Profile %q exists. Replace it?", name)); err != nil || !ok {
			return "", errors.Join(err, errors.New("aborted"))
		}
	}
	var prof config.Profile
	if prof.KeyID, err = p.ask("Key ID", ""); err != nil {
		return "", err
	}
	if prof.KeyID == "" {
		return "", errors.New("key id is required")
	}
	keyPath, err := p.ask("Path to the .p8 private key", "")
	if err != nil {
		return "", err
	}
	if keyPath == "" {
		return "", errors.New("key path is required")
	}
	if keyPath, err = absPath(keyPath); err != nil {
		return "", err
	}
	if id, bad := keyIDMismatch(prof.KeyID, keyPath); bad {
		fmt.Fprintf(stderr, "warning: key id %s doesn't match the file name (AuthKey_%s.p8)\n", prof.KeyID, id)
	}
	if _, err := auth.ResolveKey(context.Background(), prof.KeyID, auth.FileSource{Path: keyPath}); err != nil {
		return "", fmt.Errorf("check key: %w", err)
	}
	if err := askKeyStorage(p, &prof, keyPath); err != nil {
		return "", err
	}
	if err := askCommon(p, &prof, o, stderr); err != nil {
		return "", err
	}
	return name, saveProfile(path, cfg, name, prof, stdout)
}

func absPath(p string) (string, error) {
	abs, err := filepath.Abs(config.ExpandHome(p, os.Getenv))
	if err != nil {
		return "", fmt.Errorf("key path: %w", err)
	}
	return abs, nil
}

func authImport(p *prompter, o addOptions, stdout, stderr io.Writer) (string, error) {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	cands := config.Discover(home, cwd, os.Getenv)
	if len(cands) == 0 {
		return "", errors.New("no credentials found (fastlane/api_key.json, ASC_* env, AuthKey_*.p8); use `hamn auth add`")
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
		return "", err
	}
	n, err := strconv.Atoi(pick)
	if err != nil || n < 1 || n > len(cands) {
		return "", fmt.Errorf("invalid choice %q", pick)
	}
	c := cands[n-1]
	prof := c.Profile
	name, err := p.ask("Profile name", "default")
	if err != nil {
		return "", err
	}
	switch c.Key.Source {
	case config.KeyFromFile:
		keyPath, err := absPath(c.Key.Path)
		if err != nil {
			return "", err
		}
		if err := askKeyStorage(p, &prof, keyPath); err != nil {
			return "", err
		}
	case config.KeyFromKeychain:
		prof.KeySource = config.KeySourceKeychain
	case config.KeyFromEnv, config.KeyFromFastlaneJSON:
		// Config never holds key material, so inline keys must move to the keychain.
		ok, err := p.yes(fmt.Sprintf("The key is inline (%s). Store it in the OS keychain?", c.Key))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New("aborted: an inline key must be stored in the keychain or saved as a .p8 file")
		}
		data, err := c.Key.Read(os.Getenv)
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		if err := storeInKeychain(&prof, data); err != nil {
			return "", err
		}
	default:
		keyPath, err := p.ask("Path to the .p8 private key", "")
		if err != nil {
			return "", err
		}
		if keyPath, err = absPath(keyPath); err != nil {
			return "", err
		}
		if err := askKeyStorage(p, &prof, keyPath); err != nil {
			return "", err
		}
	}
	if err := askCommon(p, &prof, o, stderr); err != nil {
		return "", err
	}
	return name, saveProfile(path, cfg, name, prof, stdout)
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
		return c, nil, err // the asc error already names the request
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

// envCredentialsSet reports whether ASC_* (or fastlane) env credentials are
// present, which makes "env" the active profile.
func envCredentialsSet() bool {
	return strings.TrimSpace(os.Getenv(config.EnvKeyID)) != "" || strings.TrimSpace(os.Getenv(config.FastlaneKeyID)) != ""
}

// setTarget picks the profile `auth set` edits when no name is given: env
// when ASC_KEY_ID is set (it wins at runtime), else the default or only profile.
func setTarget(cfg *config.Config) (string, error) {
	switch {
	case envCredentialsSet():
		return config.EnvProfileName, nil
	case cfg.DefaultProfile != "":
		return cfg.DefaultProfile, nil
	case len(cfg.Profiles) == 1:
		return cfg.ProfileNames()[0], nil
	case len(cfg.Profiles) == 0:
		return "", errors.New("no profiles yet: run `hamn auth import` or `hamn auth add`")
	default:
		return "", usageErr("several profiles; name one: hamn auth set <" + strings.Join(cfg.ProfileNames(), "|") + "> key=value")
	}
}

func authSet(name string, pairs []string, stdout, stderr io.Writer) error {
	path, cfg, err := loadConfig(stderr)
	if err != nil {
		return err
	}
	if name == "" {
		if name, err = setTarget(cfg); err != nil {
			return err
		}
	}
	if name == config.EnvProfileName {
		return authSetEnv(path, cfg, pairs, stdout)
	}
	prof, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("no profile %q (known: %s)", name, strings.Join(cfg.ProfileNames(), ", "))
	}
	before := prof
	for _, kv := range pairs {
		note, err := setField(&prof, kv)
		if err != nil {
			return err
		}
		if note != "" {
			fmt.Fprintln(stdout, "note:", note)
		}
	}
	if prof == before {
		fmt.Fprintf(stdout, "profile %q unchanged\n", name)
		return nil
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

// authSetEnv stores settings for the env profile: ASC_* variables carry the
// key, the config keeps what MCP client env blocks usually lack.
func authSetEnv(path string, cfg *config.Config, pairs []string, stdout io.Writer) error {
	s := config.EnvSettings{}
	if cfg.Env != nil {
		s = *cfg.Env
	}
	before := s
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok:
			return usageErr(fmt.Sprintf("want key=value, got %q", kv))
		case k == "vendor_number":
			s.VendorNumber = strings.TrimSpace(v)
		case k == "default_app":
			s.DefaultApp = strings.TrimSpace(v)
		default:
			return usageErr(fmt.Sprintf("the env profile stores only vendor_number and default_app; set %s through ASC_* variables, or run `hamn auth import` to save a full profile", k))
		}
	}
	if s == before {
		fmt.Fprintln(stdout, "env settings unchanged")
		return nil
	}
	cfg.Env = &s
	if s == (config.EnvSettings{}) {
		cfg.Env = nil
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "updated env settings in %s (ASC_VENDOR_NUMBER still wins when set)\n", path)
	return nil
}

// setField applies one key=value assignment to prof and returns a note when
// it also changed the key kind.
func setField(prof *config.Profile, kv string) (string, error) {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return "", usageErr(fmt.Sprintf("want key=value, got %q", kv))
	}
	switch k {
	case "vendor_number":
		prof.VendorNumber = v
	case "default_app":
		prof.DefaultApp = v
	case "issuer_id":
		prof.IssuerID = v
		switch {
		case v != "" && prof.KeyKind != tool.KeyTeam:
			was := prof.KeyKind
			prof.KeyKind = tool.KeyTeam
			if was == tool.KeyIndividual {
				return "an issuer id means a team key; key_kind is now team", nil
			}
		case v == "" && prof.KeyKind != tool.KeyIndividual:
			prof.KeyKind = tool.KeyIndividual
			return "no issuer id means an Individual key; key_kind is now individual", nil
		}
	case "allow_writes":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", usageErr(fmt.Sprintf("allow_writes: %v", err))
		}
		prof.AllowWrites = b
	case "key_kind":
		kind := tool.KeyKind(v)
		if kind != tool.KeyTeam && kind != tool.KeyIndividual {
			return "", usageErr("key_kind must be team or individual")
		}
		prof.KeyKind = kind
		if kind == tool.KeyIndividual && prof.IssuerID != "" {
			prof.IssuerID = ""
			return "Individual keys have no issuer; cleared issuer_id", nil
		}
	default:
		return "", usageErr(fmt.Sprintf("unknown key %q (vendor_number, default_app, allow_writes, issuer_id, key_kind)", k))
	}
	return "", nil
}
