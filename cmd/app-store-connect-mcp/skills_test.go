package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/skills"
)

func runSkillsCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), append([]string{"skills"}, args...), strings.NewReader(""), &out, &errOut)
	return out.String(), err
}

func TestSkillsInstall(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	all, err := skills.All()
	if err != nil || len(all) != 6 {
		t.Fatalf("skills.All() = %d skills, %v", len(all), err)
	}

	out, err := runSkillsCmd(t, "install")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".claude", "skills")
	for _, s := range all {
		p := filepath.Join(root, s.Name, "SKILL.md")
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, s.Content) {
			t.Fatalf("%s not installed: %v", p, err)
		}
		if !strings.Contains(out, "installed") || !strings.Contains(out, p) {
			t.Fatalf("output does not name %s:\n%s", p, out)
		}
	}

	// Rerun: nothing to do.
	out, err = runSkillsCmd(t, "install")
	if err != nil || strings.Count(out, "unchanged") != len(all) {
		t.Fatalf("rerun: %v\n%s", err, out)
	}

	// A locally edited skill is kept without --force.
	edited := filepath.Join(root, "app-store-connect-mcp-release", "SKILL.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runSkillsCmd(t, "install")
	if err == nil || !strings.Contains(out, "skipped") {
		t.Fatalf("modified file: err=%v\n%s", err, out)
	}
	var ue *usageError
	if errors.As(err, &ue) {
		t.Fatalf("refusal must not be a usage error: %v", err)
	}
	if got, _ := os.ReadFile(edited); string(got) != "mine" {
		t.Fatal("modified skill overwritten without --force")
	}
	out, err = runSkillsCmd(t, "install", "--force")
	if err != nil || !strings.Contains(out, "updated") {
		t.Fatalf("--force: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(edited); string(got) == "mine" {
		t.Fatal("--force did not overwrite")
	}
}

func TestSkillsInstallClientsAndDir(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	for client, rel := range map[string]string{
		"codex":  ".codex/skills",
		"cursor": ".cursor/skills",
		"omp":    ".omp/agent/skills",
	} {
		if _, err := runSkillsCmd(t, "install", "--client", client); err != nil {
			t.Fatalf("%s: %v", client, err)
		}
		if _, err := os.Stat(filepath.Join(home, rel, "app-store-connect-mcp-sales", "SKILL.md")); err != nil {
			t.Fatalf("%s: %v", client, err)
		}
	}
	dir := filepath.Join(t.TempDir(), "custom")
	if _, err := runSkillsCmd(t, "install", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app-store-connect-mcp-reviews", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsUsageErrors(t *testing.T) {
	setHome(t, t.TempDir())
	for _, args := range [][]string{
		{},
		{"nope"},
		{"install", "--client", "emacs"},
		{"install", "extra"},
		{"list", "extra"},
	} {
		_, err := runSkillsCmd(t, args...)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("skills %v: want usage error, got %v", args, err)
		}
	}
}

func TestSkillsList(t *testing.T) {
	out, err := runSkillsCmd(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app-store-connect-mcp-release", "app-store-connect-mcp-testflight", "app-store-connect-mcp-reviews", "app-store-connect-mcp-metadata-translate", "app-store-connect-mcp-screenshots", "app-store-connect-mcp-sales"} {
		if !strings.Contains(out, name+"\t") {
			t.Errorf("list misses %s:\n%s", name, out)
		}
	}
}
