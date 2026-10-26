package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	_ "github.com/idoo/app-store-connect-mcp/internal/tools/all" // registers every tool for the permissions list.
)

// clients lists the supported `config snippet|install` targets.
var clients = []string{"claude-code", "claude-desktop", "codex", "cursor", "vscode", "windsurf"}

// serverName is the MCP server name used in every client config.
const serverName = "app-store-connect-mcp"

// sessionTools are read-only tools the server adds per session (not in the
// tool registry).
var sessionTools = []string{"toolsets"}

// configUsage is printed for `app-store-connect-mcp config --help` and misuse.
var configUsage = "usage: app-store-connect-mcp config <snippet|install> <client> [--profile NAME] [--allow-writes]\nclients: " + strings.Join(clients, ", ")

// credentialsNote reminds that GUI clients don't inherit shell env.
const credentialsNote = "credentials: run `app-store-connect-mcp auth import` (or `app-store-connect-mcp auth add`) once; GUI clients don't see ASC_* from your shell"

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(configUsage)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o serverOpts
	fs.StringVar(&o.profile, "profile", "", "pin the server to this profile via ASC_MCP_PROFILE")
	fs.BoolVar(&o.writes, "allow-writes", false, "turn writes on for this client only (sets "+config.EnvAllowWrites+"=1 in its entry)")
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return usageErr(configUsage)
	}
	client := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return usageErr(err.Error())
	}
	if !slices.Contains(clients, client) {
		return usageErr(fmt.Sprintf("unknown client %q; one of: %s", client, strings.Join(clients, ", ")))
	}
	bin, err := binaryPath()
	if err != nil {
		return err
	}
	switch sub {
	case "snippet":
		s, err := snippet(client, bin, o, readToolPermissions())
		if err != nil {
			return err
		}
		// The credentials step comes first, and only while it is still
		// needed; stderr keeps stdout pasteable.
		if note := credentialsHint(stderr); note != "" {
			fmt.Fprintln(stderr, note)
		}
		_, err = io.WriteString(stdout, s)
		return err
	case "install":
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		if err := install(client, bin, o, home, stdout); err != nil {
			return err
		}
		warnNoProfile(stdout, stderr)
		return nil
	default:
		return usageErr(fmt.Sprintf("unknown config subcommand %q (snippet, install)", sub))
	}
}

// warnNoProfile tells the user the installed server has no credentials yet.
func warnNoProfile(stdout, stderr io.Writer) {
	if note := credentialsHint(stderr); note != "" {
		fmt.Fprintln(stdout, note)
	}
}

// credentialsHint is the credentials step still to do: "" when a saved
// profile exists; the note alone when ASC_* env is set (GUI clients don't
// see it); a no-profile warning plus the note otherwise.
func credentialsHint(stderr io.Writer) string {
	if envCredentialsSet() {
		return credentialsNote
	}
	path, cfg, err := loadConfig(stderr)
	if err != nil || len(cfg.Profiles) > 0 {
		return ""
	}
	return fmt.Sprintf("warning: no profile in %s yet, so every tool will fail.\n%s", path, credentialsNote)
}

// binaryPath returns the path clients should launch: a stable PATH location
// (e.g. /opt/homebrew/bin/app-store-connect-mcp), never the versioned Homebrew Cellar path,
// which `brew upgrade` deletes.
func binaryPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate app-store-connect-mcp binary: %w", err)
	}
	return stableBinaryPath(p, exec.LookPath), nil
}

// stableBinaryPath prefers the PATH entry for exe, then the Homebrew prefix
// bin for a Cellar path, else exe as invoked.
func stableBinaryPath(exe string, lookPath func(string) (string, error)) string {
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return exe
	}
	if lp, err := lookPath(serverName); err == nil {
		if abs, err := filepath.Abs(lp); err == nil {
			if fi, err := os.Stat(abs); err == nil && os.SameFile(fi, exeInfo) {
				return abs
			}
		}
	}
	if i := strings.Index(filepath.ToSlash(exe), "/Cellar/"); i >= 0 {
		cand := filepath.Join(exe[:i], "bin", filepath.Base(exe))
		if fi, err := os.Stat(cand); err == nil && os.SameFile(fi, exeInfo) {
			return cand
		}
	}
	return exe
}

// serverOpts are the per-client server settings `config` writes.
type serverOpts struct {
	// profile pins the server to a profile via ASC_MCP_PROFILE.
	profile string
	// writes turns writes on for this client entry only.
	writes bool
}

// env returns the server env: profile selection and write mode, never key material.
func (o serverOpts) env() map[string]string {
	env := map[string]string{}
	if o.profile != "" {
		env[config.EnvProfile] = o.profile
	}
	if o.writes {
		env[config.EnvAllowWrites] = "1"
	}
	return env
}

// readToolPermissions lists `mcp__app-store-connect-mcp__<tool>` for every read-class tool.
func readToolPermissions() []string {
	var out []string
	for _, t := range tool.All() {
		if t.Meta().Class == tool.Read {
			out = append(out, "mcp__"+serverName+"__"+t.Name())
		}
	}
	for _, n := range sessionTools {
		out = append(out, "mcp__"+serverName+"__"+n)
	}
	slices.Sort(out)
	return out
}

// serverEntry is the JSON server entry for client.
func serverEntry(client, bin string, o serverOpts) map[string]any {
	e := map[string]any{"command": bin, "args": []string{}}
	if client == "vscode" {
		e["type"] = "stdio"
	}
	if env := o.env(); len(env) > 0 {
		e["env"] = env
	}
	return e
}

// serversKey is the top-level JSON key holding MCP servers for client.
func serversKey(client string) string {
	if client == "vscode" {
		return "servers"
	}
	return "mcpServers"
}

// snippet renders the config for client; perms is the claude-code
// permissions allow list.
func snippet(client, bin string, o serverOpts, perms []string) (string, error) {
	switch client {
	case "claude-code":
		var b strings.Builder
		b.WriteString("# Register app-store-connect-mcp for every project:\n")
		b.WriteString(shellJoin(claudeAddArgs(bin, o)...) + "\n\n")
		b.WriteString("# Optional: pre-approve read-only tools in ~/.claude/settings.json:\n")
		perm, err := json.MarshalIndent(map[string]any{"permissions": map[string]any{"allow": perms}}, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode permissions: %w", err)
		}
		b.Write(perm)
		b.WriteString("\n")
		return b.String(), nil
	case "codex":
		return "# ~/.codex/config.toml\n" + codexBlock(bin, o), nil
	default:
		doc := map[string]any{serversKey(client): map[string]any{serverName: serverEntry(client, bin, o)}}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode snippet: %w", err)
		}
		return "// " + snippetHints[client] + "\n" + string(out) + "\n", nil
	}
}

func claudeAddArgs(bin string, o serverOpts) []string {
	args := []string{"claude", "mcp", "add", serverName, "-s", "user"}
	env := o.env()
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, "-e", k+"="+env[k])
	}
	return append(args, "--", bin)
}

func shellJoin(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?&;|<>()[]{}!#~") {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// Codex timeouts: Codex's defaults (10 s startup, 60 s per tool) cut off
// builds_wait and report fan-outs before app-store-connect-mcp's own limits.
const (
	codexStartupTimeout = "startup_timeout_sec = 20"
	codexToolTimeout    = "tool_timeout_sec = 330"
)

// codexBlock renders the [mcp_servers.app-store-connect-mcp] table.
func codexBlock(bin string, o serverOpts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = %s\nargs = []\n%s\n%s\n", serverName, tomlString(bin), codexStartupTimeout, codexToolTimeout)
	if env := o.env(); len(env) > 0 {
		pairs := make([]string, 0, len(env))
		for _, k := range slices.Sorted(maps.Keys(env)) {
			pairs = append(pairs, k+" = "+tomlString(env[k]))
		}
		fmt.Fprintf(&b, "env = { %s }\n", strings.Join(pairs, ", "))
	}
	return b.String()
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // JSON string escapes are valid TOML basic-string escapes.
	return string(b)
}

// snippetHints say where each JSON snippet goes (OS-independent wording).
var snippetHints = map[string]string{
	"claude-desktop": "Claude Desktop: Settings > Developer > Edit Config (claude_desktop_config.json)",
	"cursor":         "~/.cursor/mcp.json",
	"vscode":         "VS Code: run \"MCP: Open User Configuration\" (mcp.json)",
	"windsurf":       "~/.codeium/windsurf/mcp_config.json",
}

// clientConfigPath returns the config file a client reads, under home.
func clientConfigPath(client, home string) string {
	switch client {
	case "cursor":
		return filepath.Join(home, ".cursor", "mcp.json")
	case "windsurf":
		return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	case "codex":
		return filepath.Join(home, ".codex", "config.toml")
	case "claude-desktop":
		return filepath.Join(appSupportDir(home), "Claude", "claude_desktop_config.json")
	case "vscode":
		return filepath.Join(appSupportDir(home), "Code", "User", "mcp.json")
	default:
		return ""
	}
}

// appSupportDir is the per-user application config directory.
func appSupportDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming")
	default:
		return filepath.Join(home, ".config")
	}
}

// claudeTimeout bounds `claude mcp add`, which can hang on a first run.
var claudeTimeout = 20 * time.Second

// runClaude runs the claude CLI without stdin; tests replace it.
var runClaude = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // fixed program, our own args.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, stdout, stderr
	return cmd.Run()
}

// installClaudeCode registers the server with `claude mcp add`. An existing
// registration is never replaced, since that would drop the user's -e flags;
// the replace command is printed instead.
func installClaudeCode(bin string, o serverOpts, stdout io.Writer) error {
	args := claudeAddArgs(bin, o)
	ctx, cancel := context.WithTimeout(context.Background(), claudeTimeout)
	defer cancel()
	if runClaude(ctx, []string{"claude", "mcp", "get", serverName}, io.Discard, io.Discard) == nil {
		fmt.Fprintf(stdout, "%s is already registered in Claude Code; to replace it:\n  claude mcp remove %s -s user && %s\n", serverName, serverName, shellJoin(args...))
		return nil
	}
	err := runClaude(ctx, args, stdout, os.Stderr)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		fmt.Fprintf(stdout, "Claude Code CLI not found. Run this once it's installed (or see `app-store-connect-mcp config snippet claude-code`):\n  %s\n", shellJoin(args...))
		return nil
	case ctx.Err() != nil:
		fmt.Fprintf(stdout, "`claude mcp add` didn't finish in %s. Run it yourself:\n  %s\n", claudeTimeout, shellJoin(args...))
		return fmt.Errorf("claude mcp add timed out after %s", claudeTimeout)
	default:
		fmt.Fprintf(stdout, "`claude mcp add` failed. Run it yourself:\n  %s\n", shellJoin(args...))
		return fmt.Errorf("claude mcp add: %w", err)
	}
}

func install(client, bin string, o serverOpts, home string, stdout io.Writer) error {
	if client == "claude-code" {
		return installClaudeCode(bin, o, stdout)
	}
	path := clientConfigPath(client, home)
	var (
		res mergeResult
		err error
	)
	if client == "codex" {
		res, err = mergeFile(path, func(old []byte) ([]byte, error) { return mergeCodexTOML(old, codexBlock(bin, o)) })
	} else {
		res, err = mergeFile(path, func(old []byte) ([]byte, error) {
			return mergeJSONServer(old, serversKey(client), serverName, serverEntry(client, bin, o))
		})
	}
	if err != nil {
		return err
	}
	if res.unchanged {
		fmt.Fprintf(stdout, "already installed in %s (unchanged)\n", path)
		return nil
	}
	if res.backup != "" {
		fmt.Fprintf(stdout, "backup: %s\n", res.backup)
	}
	fmt.Fprintf(stdout, "installed %s into %s\n", serverName, path)
	return nil
}

// mergeResult says what mergeFile did.
type mergeResult struct {
	backup    string // backup file name, empty when none was made
	unchanged bool   // the file already had this content; nothing written
}

// mergeFile rewrites path with merge(old). An existing file is first copied
// to path.app-store-connect-mcp-backup-<timestamp>. The new content is written atomically,
// keeping the old file mode (0600 for new files). Identical content is left
// alone: no backup, no write.
func mergeFile(path string, merge func(old []byte) ([]byte, error)) (mergeResult, error) {
	old, err := os.ReadFile(path) //nolint:gosec // client config path we chose.
	mode := os.FileMode(0o600)
	exists := err == nil
	switch {
	case exists:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return mergeResult{}, fmt.Errorf("read %s: %w", path, err)
	}
	out, err := merge(old)
	if err != nil {
		return mergeResult{}, fmt.Errorf("%s: %w", path, err)
	}
	if exists && bytes.Equal(out, old) {
		return mergeResult{unchanged: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return mergeResult{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	backup := ""
	if exists {
		backup = path + ".app-store-connect-mcp-backup-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, old, mode); err != nil { //nolint:gosec // backup next to the client config path we chose.
			return mergeResult{}, fmt.Errorf("backup %s: %w", path, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return mergeResult{}, fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err = tmp.Write(out); err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
		return mergeResult{}, fmt.Errorf("write %s: %w", path, err)
	}
	return mergeResult{backup: backup}, nil
}

// mergeJSONServer sets doc[key][name] = entry, keeping every other key and
// server. Empty input starts a new document.
func mergeJSONServer(old []byte, key, name string, entry map[string]any) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &doc); err != nil {
			return nil, fmt.Errorf("parse JSON (edit it by hand or fix it first): %w", err)
		}
	}
	servers := map[string]any{}
	if v, ok := doc[key]; ok && v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q is not an object", key)
		}
		servers = m
	}
	servers[name] = entry
	doc[key] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return append(out, '\n'), nil
}

// mergeCodexTOML replaces the [mcp_servers.app-store-connect-mcp] table (and its sub-tables)
// with block, keeping every other line, including comments. Timeouts the user
// set in the old table are kept. The result must parse as TOML.
func mergeCodexTOML(old []byte, block string) ([]byte, error) {
	if len(old) > 0 {
		var probe map[string]any
		if err := toml.Unmarshal(old, &probe); err != nil {
			return nil, fmt.Errorf("parse TOML (fix it first): %w", err)
		}
	}
	own := "mcp_servers." + serverName
	var kept []string
	userTimeouts := map[string]string{}
	skipping, inOwn := false, false
	for _, line := range strings.SplitAfter(string(old), "\n") {
		if name, ok := tableHeader(line); ok {
			skipping = name == own || strings.HasPrefix(name, own+".")
			inOwn = name == own
		}
		if inOwn {
			if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				if k = strings.TrimSpace(k); k == "startup_timeout_sec" || k == "tool_timeout_sec" {
					userTimeouts[k] = strings.TrimRight(strings.TrimSpace(line), "\r")
				}
			}
		}
		if !skipping && line != "" {
			kept = append(kept, line)
		}
	}
	for k, line := range userTimeouts {
		for _, def := range []string{codexStartupTimeout, codexToolTimeout} {
			if strings.HasPrefix(def, k+" ") {
				block = strings.Replace(block, def, line, 1)
			}
		}
	}
	out := strings.TrimRight(strings.Join(kept, ""), "\n")
	if out != "" {
		out += "\n\n"
	}
	out += block
	var probe map[string]any
	if err := toml.Unmarshal([]byte(out), &probe); err != nil {
		return nil, fmt.Errorf("merged TOML is invalid: %w", err)
	}
	return []byte(out), nil
}

// tableHeader parses a TOML [table] or [[array]] header line into its
// dotted name with whitespace and quotes removed.
func tableHeader(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if i := strings.Index(s, "#"); i >= 0 && !strings.HasPrefix(s, "#") {
		s = strings.TrimSpace(s[:i])
	}
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return "", false
	}
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, "."), true
}
