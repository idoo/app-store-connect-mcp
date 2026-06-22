package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/tool"
	_ "github.com/idoo/hamn/internal/tools/all" // registers every tool for the permissions list.
)

// clients lists the supported `config snippet|install` targets.
var clients = []string{"claude-code", "claude-desktop", "codex", "cursor", "vscode", "windsurf"}

// serverName is the MCP server name used in every client config.
const serverName = "hamn"

// sessionTools are read-only tools the server adds per session (not in the
// tool registry).
var sessionTools = []string{"toolsets"}

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("usage: hamn config <snippet|install> <client> [--profile NAME]\nclients: " + strings.Join(clients, ", "))
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "pin the server to this profile via HAMN_PROFILE")
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return usageErr(fmt.Sprintf("usage: hamn config %s <client> [--profile NAME]\nclients: %s", sub, strings.Join(clients, ", ")))
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
		s, err := snippet(client, bin, *profile, readToolPermissions())
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, s)
		return err
	case "install":
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		return install(client, bin, *profile, home, stdout)
	default:
		return usageErr(fmt.Sprintf("unknown config subcommand %q (snippet, install)", sub))
	}
}

// binaryPath returns the absolute, symlink-resolved path of this executable.
func binaryPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate hamn binary: %w", err)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// profileEnv returns the server env: only profile selection, never key material.
func profileEnv(profile string) map[string]string {
	env := map[string]string{}
	if profile != "" {
		env[config.EnvProfile] = profile
	}
	return env
}

// readToolPermissions lists `mcp__hamn__<tool>` for every read-class tool.
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
func serverEntry(client, bin, profile string) map[string]any {
	e := map[string]any{"command": bin, "args": []string{}}
	if client == "vscode" {
		e["type"] = "stdio"
	}
	if env := profileEnv(profile); len(env) > 0 {
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
func snippet(client, bin, profile string, perms []string) (string, error) {
	switch client {
	case "claude-code":
		var b strings.Builder
		b.WriteString("# Register hamn for every project:\n")
		b.WriteString(shellJoin(claudeAddArgs(bin, profile)...) + "\n\n")
		b.WriteString("# Optional: pre-approve read-only tools in ~/.claude/settings.json:\n")
		perm, err := json.MarshalIndent(map[string]any{"permissions": map[string]any{"allow": perms}}, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode permissions: %w", err)
		}
		b.Write(perm)
		b.WriteString("\n")
		return b.String(), nil
	case "codex":
		return "# ~/.codex/config.toml\n" + codexBlock(bin, profile), nil
	default:
		doc := map[string]any{serversKey(client): map[string]any{serverName: serverEntry(client, bin, profile)}}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode snippet: %w", err)
		}
		return "// " + snippetHints[client] + "\n" + string(out) + "\n", nil
	}
}

func claudeAddArgs(bin, profile string) []string {
	args := []string{"claude", "mcp", "add", serverName, "-s", "user"}
	if profile != "" {
		args = append(args, "-e", config.EnvProfile+"="+profile)
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

// codexBlock renders the [mcp_servers.hamn] table.
func codexBlock(bin, profile string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = %s\nargs = []\n", serverName, tomlString(bin))
	if profile != "" {
		fmt.Fprintf(&b, "env = { %s = %s }\n", config.EnvProfile, tomlString(profile))
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

func install(client, bin, profile, home string, stdout io.Writer) error {
	if client == "claude-code" {
		args := claudeAddArgs(bin, profile)
		cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // fixed program, our own args.
		cmd.Stdout, cmd.Stderr = stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", shellJoin(args...), err)
		}
		return nil
	}
	path := clientConfigPath(client, home)
	var (
		backup string
		err    error
	)
	if client == "codex" {
		backup, err = mergeFile(path, func(old []byte) ([]byte, error) { return mergeCodexTOML(old, codexBlock(bin, profile)) })
	} else {
		backup, err = mergeFile(path, func(old []byte) ([]byte, error) {
			return mergeJSONServer(old, serversKey(client), serverName, serverEntry(client, bin, profile))
		})
	}
	if err != nil {
		return err
	}
	if backup != "" {
		fmt.Fprintf(stdout, "backup: %s\n", backup)
	}
	fmt.Fprintf(stdout, "installed %s into %s\n", serverName, path)
	return nil
}

// mergeFile rewrites path with merge(old). An existing file is first copied
// to path.hamn-backup-<timestamp>, whose name is returned. The new content is
// written atomically, keeping the old file mode (0600 for new files).
func mergeFile(path string, merge func(old []byte) ([]byte, error)) (string, error) {
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
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	out, err := merge(old)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	backup := ""
	if exists {
		backup = path + ".hamn-backup-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, old, mode); err != nil { //nolint:gosec // backup next to the client config path we chose.
			return "", fmt.Errorf("backup %s: %w", path, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
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
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return backup, nil
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

// mergeCodexTOML replaces the [mcp_servers.hamn] table (and its sub-tables)
// with block, keeping every other line, including comments. The result must
// parse as TOML.
func mergeCodexTOML(old []byte, block string) ([]byte, error) {
	if len(old) > 0 {
		var probe map[string]any
		if err := toml.Unmarshal(old, &probe); err != nil {
			return nil, fmt.Errorf("parse TOML (fix it first): %w", err)
		}
	}
	own := "mcp_servers." + serverName
	var kept []string
	skipping := false
	for _, line := range strings.SplitAfter(string(old), "\n") {
		if name, ok := tableHeader(line); ok {
			skipping = name == own || strings.HasPrefix(name, own+".")
		}
		if !skipping && line != "" {
			kept = append(kept, line)
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
