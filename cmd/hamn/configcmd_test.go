package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"

	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/tool"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fakeBin = "/ABS/PATH/hamn"

func TestSnippetGoldens(t *testing.T) {
	perms := []string{"mcp__hamn__apps_list", "mcp__hamn__doctor"}
	for _, client := range clients {
		for _, profile := range []string{"", "work"} {
			name := client
			if profile != "" {
				name += "-" + profile
			}
			t.Run(name, func(t *testing.T) {
				got, err := snippet(client, fakeBin, profile, perms)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join("testdata", "snippets", name+".golden")
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if diff := cmp.Diff(string(want), got); diff != "" {
					t.Errorf("snippet mismatch (-want +got):\n%s", diff)
				}
				if strings.Contains(got, "ASC_") || strings.Contains(got, "PRIVATE") {
					t.Error("snippet must not reference key material")
				}
			})
		}
	}
}

func TestReadToolPermissionsOnlyReadTools(t *testing.T) {
	perms := readToolPermissions()
	if len(perms) == 0 {
		t.Fatal("no permissions")
	}
	for _, p := range perms {
		name := strings.TrimPrefix(p, "mcp__hamn__")
		if name == "toolsets" {
			continue
		}
		tl, ok := tool.Lookup(name)
		if !ok || tl.Meta().Class != tool.Read {
			t.Errorf("%s is not a registered read tool", p)
		}
	}
}

func TestMergeJSONServerPreservesOthers(t *testing.T) {
	old := []byte(`{"theme":"dark","mcpServers":{"other":{"command":"x"},"hamn":{"command":"old"}}}`)
	out, err := mergeJSONServer(old, "mcpServers", "hamn", serverEntry("cursor", fakeBin, ""))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"theme": "dark",
		"mcpServers": map[string]any{
			"other": map[string]any{"command": "x"},
			"hamn":  map[string]any{"command": fakeBin, "args": []any{}},
		},
	}
	if diff := cmp.Diff(want, doc); diff != "" {
		t.Errorf("merge (-want +got):\n%s", diff)
	}
}

func TestMergeJSONServerRejects(t *testing.T) {
	for name, in := range map[string]string{
		"invalid":    `{"mcpServers":`,
		"not object": `{"mcpServers":[]}`,
		"jsonc":      "// comment\n{}",
	} {
		if _, err := mergeJSONServer([]byte(in), "mcpServers", "hamn", map[string]any{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestInstallJSONBacksUp(t *testing.T) {
	home := t.TempDir()
	path := clientConfigPath("cursor", home)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"mcpServers":{"other":{"command":"x"}}}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := install("cursor", fakeBin, "work", home, &out); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(path + ".hamn-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != string(old) {
		t.Errorf("backup = %q", b)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["other"].Command != "x" || doc.MCPServers["hamn"].Command != fakeBin || doc.MCPServers["hamn"].Env["HAMN_PROFILE"] != "work" {
		t.Errorf("merged = %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", fi.Mode().Perm())
	}
}

func TestInstallCreatesNewFileWithoutBackup(t *testing.T) {
	home := t.TempDir()
	var out strings.Builder
	if err := install("vscode", fakeBin, "", home, &out); err != nil {
		t.Fatal(err)
	}
	path := clientConfigPath("vscode", home)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"servers"`) || !strings.Contains(string(b), `"type": "stdio"`) {
		t.Errorf("vscode config = %s", b)
	}
	if backups, _ := filepath.Glob(path + ".hamn-backup-*"); len(backups) != 0 {
		t.Errorf("unexpected backups %v", backups)
	}
}

func TestMergeCodexTOML(t *testing.T) {
	old := `# my codex config
model = "o3"

[mcp_servers.other]
command = "other"

[mcp_servers.hamn]
command = "/old/hamn"

[mcp_servers.hamn.env]
HAMN_PROFILE = "old"

[profiles.fast] # trailing comment
model = "mini"
`
	out, err := mergeCodexTOML([]byte(old), codexBlock(fakeBin, "work"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "# my codex config") || !strings.Contains(s, "[profiles.fast] # trailing comment") {
		t.Errorf("comments or tables lost:\n%s", s)
	}
	if strings.Contains(s, "/old/hamn") || strings.Contains(s, `"old"`) {
		t.Errorf("old hamn table kept:\n%s", s)
	}
	var doc struct {
		Model      string `toml:"model"`
		MCPServers map[string]struct {
			Command string            `toml:"command"`
			Env     map[string]string `toml:"env"`
		} `toml:"mcp_servers"`
		Profiles map[string]map[string]string `toml:"profiles"`
	}
	if err := toml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Model != "o3" || doc.MCPServers["other"].Command != "other" || doc.MCPServers["hamn"].Command != fakeBin ||
		doc.MCPServers["hamn"].Env["HAMN_PROFILE"] != "work" || doc.Profiles["fast"]["model"] != "mini" {
		t.Errorf("merged = %+v", doc)
	}
}

func TestMergeCodexTOMLEmptyAndInvalid(t *testing.T) {
	out, err := mergeCodexTOML(nil, codexBlock(fakeBin, ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != codexBlock(fakeBin, "") {
		t.Errorf("empty merge = %q", out)
	}
	if _, err := mergeCodexTOML([]byte("[broken"), codexBlock(fakeBin, "")); err == nil {
		t.Error("want error for invalid TOML")
	}
}

func TestSetField(t *testing.T) {
	var p struct{ ok, bad []string }
	p.ok = []string{"vendor_number=123", "default_app=com.x", "allow_writes=true", "issuer_id=i", "key_kind=individual"}
	p.bad = []string{"nokey", "allow_writes=maybe", "key_kind=other", "key_path=/x"}
	for _, kv := range p.ok {
		if _, err := setField(new(config.Profile), kv); err != nil {
			t.Errorf("%s: %v", kv, err)
		}
	}
	for _, kv := range p.bad {
		if _, err := setField(new(config.Profile), kv); err == nil {
			t.Errorf("%s: want error", kv)
		}
	}
}
