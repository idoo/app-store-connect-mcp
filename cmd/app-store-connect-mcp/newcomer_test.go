package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/config"
)

// writeProfile saves a "default" profile whose key file exists.
func writeProfile(t *testing.T, home, cfgPath string) {
	t.Helper()
	key := writeKey(t, home, "SAVED1")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "default_profile = \"default\"\n\n[profiles.default]\nkey_id = \"SAVED1\"\nissuer_id = \"" + testIssuer + "\"\nkey_path = " + tomlString(key) + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// N3 + N8: env credentials shadowing a saved profile are named, and a
// missing .p8 says how to fix key_path.
func TestDoctorEnvOverrideAndMissingKey(t *testing.T) {
	home, cfgPath := sandbox(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	writeProfile(t, home, cfgPath)
	t.Setenv(config.EnvKeyID, "ZZZZZZZZZZ")
	t.Setenv(config.EnvIssuerID, testIssuer)
	t.Setenv(config.EnvKeyPath, filepath.Join(home, "Downloads", "AuthKey_ZZZZZZZZZZ.p8"))
	out, _, err := runCLI(t, "", "doctor")
	if err == nil {
		t.Fatal("doctor succeeded with a missing key file")
	}
	if !strings.Contains(out, `ASC_* env overrides saved profile "default" (unset ASC_KEY_ID to use it`) {
		t.Errorf("no env-override warning:\n%s", out)
	}
	if !strings.Contains(out, config.MissingKeyHint) {
		t.Errorf("no missing-key hint:\n%s", out)
	}

	// No warning without a saved profile.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	out, _, _ = runCLI(t, "", "doctor")
	if strings.Contains(out, "overrides saved profile") {
		t.Errorf("warning without a saved profile:\n%s", out)
	}
}

// N4: import names the env variables it lacks and fails clearly when stdin
// has no answers instead of "read answer: EOF".
func TestAuthImportNonInteractive(t *testing.T) {
	sandbox(t)
	t.Setenv(config.EnvKeyID, "98LMBZKZCN")
	t.Setenv("ASC_KEY_PATH", "/nowhere/AuthKey_98LMBZKZCN.p8")
	_, errOut, err := runCLI(t, "", "auth", "import", "--no-verify")
	if err == nil {
		t.Fatal("import succeeded without answers")
	}
	for _, want := range []string{"set ASC_PRIVATE_KEY_PATH or ASC_PRIVATE_KEY", "ASC_KEY_PATH is not read", "needed for sales and finance (set ASC_VENDOR_NUMBER)"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "read answer") || !strings.Contains(msg, "not interactive") || !strings.Contains(msg, "ASC_PRIVATE_KEY_PATH") {
		t.Errorf("err = %v", err)
	}
}

// N5: config install --allow-writes writes the flag into that client entry only.
func TestConfigInstallAllowWrites(t *testing.T) {
	home, _ := sandbox(t)
	if _, _, err := runCLI(t, "", "config", "install", "cursor", "--allow-writes"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ASC_MCP_ALLOW_WRITES": "1"`) {
		t.Errorf("mcp.json = %s", b)
	}
	if got := shellJoin(claudeAddArgs(fakeBin, serverOpts{profile: "work", writes: true})...); got !=
		"claude mcp add app-store-connect-mcp -s user -e ASC_MCP_ALLOW_WRITES=1 -e ASC_MCP_PROFILE=work -- "+fakeBin {
		t.Errorf("claude args = %q", got)
	}
	if b := codexBlock(fakeBin, serverOpts{writes: true}); !strings.Contains(b, `env = { ASC_MCP_ALLOW_WRITES = "1" }`) {
		t.Errorf("codex block = %s", b)
	}
	// Without the flag nothing about writes is written.
	if b, _ := snippet("cursor", fakeBin, serverOpts{}, nil); strings.Contains(b, "ALLOW_WRITES") {
		t.Errorf("snippet = %s", b)
	}
}

// N7: the credentials step is printed before the snippet, and not at all
// once a profile is saved.
func TestSnippetCredentialsStep(t *testing.T) {
	home, cfgPath := sandbox(t)
	out, errOut, err := runCLI(t, "", "config", "snippet", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "auth import") || strings.Contains(out, "auth import") {
		t.Errorf("no profile: stdout %q stderr %q", out, errOut)
	}
	writeProfile(t, home, cfgPath)
	_, errOut, err = runCLI(t, "", "config", "snippet", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut, "auth import") {
		t.Errorf("credentials hint with a saved profile: %q", errOut)
	}
}
