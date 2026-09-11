package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idoo/hamn/skills"
)

// skillClients maps a client to its user skills directory, relative to $HOME.
var skillClients = map[string]string{
	"claude-code": filepath.Join(".claude", "skills"),
	"codex":       filepath.Join(".codex", "skills"),
	"cursor":      filepath.Join(".cursor", "skills"),
	"omp":         filepath.Join(".omp", "agent", "skills"),
}

func skillClientNames() []string {
	names := make([]string, 0, len(skillClients))
	for n := range skillClients {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

const skillsUsage = "usage: hamn skills <list|install> [--client claude-code|codex|cursor|omp] [--dir PATH] [--force]"

func runSkills(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(skillsUsage)
	}
	all, err := skills.All()
	if err != nil {
		return fmt.Errorf("embedded skills: %w", err)
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "list":
		if len(rest) > 0 {
			return usageErr(skillsUsage)
		}
		for _, s := range all {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\n", s.Name, s.Description); err != nil {
				return err
			}
		}
		return nil
	case "install":
		return installSkills(all, rest, stdout, stderr)
	default:
		return usageErr(fmt.Sprintf("unknown skills subcommand %q\n%s", sub, skillsUsage))
	}
}

func installSkills(all []skills.Skill, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("skills install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "claude-code", "client whose user skills dir to use: "+strings.Join(skillClientNames(), ", "))
	dir := fs.String("dir", "", "install into this directory instead of the client's")
	force := fs.Bool("force", false, "overwrite skill files that differ from the bundled version")
	if err := fs.Parse(args); err != nil {
		return usageErr(err.Error())
	}
	if fs.NArg() > 0 {
		return usageErr(skillsUsage)
	}
	root := *dir
	if root == "" {
		rel, ok := skillClients[*client]
		if !ok {
			return usageErr(fmt.Sprintf("unknown client %q; one of: %s", *client, strings.Join(skillClientNames(), ", ")))
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		root = filepath.Join(home, rel)
	}

	var refused []string
	for _, s := range all {
		path := filepath.Join(root, s.Name, skills.FileName)
		status, err := writeSkill(path, s.Content, *force)
		if err != nil {
			return err
		}
		if status == "modified" {
			refused = append(refused, path)
			status = "skipped (modified locally; --force overwrites)"
		}
		if _, err := fmt.Fprintf(stdout, "%-12s %s\n", status, path); err != nil {
			return err
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("%d skill file(s) differ from the bundled version and were left alone; rerun with --force to overwrite", len(refused))
	}
	return nil
}

// writeSkill writes content to path unless an existing file differs and force
// is false. It returns "installed", "updated", "unchanged" or "modified".
func writeSkill(path string, content []byte, force bool) (string, error) {
	old, err := os.ReadFile(path) //nolint:gosec // path under the chosen skills dir.
	switch {
	case err == nil && bytes.Equal(old, content):
		return "unchanged", nil
	case err == nil && !force:
		return "modified", nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if old != nil {
		return "updated", nil
	}
	return "installed", nil
}
