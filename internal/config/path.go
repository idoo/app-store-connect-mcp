package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome replaces a leading "~" or "~/" in p with the home directory
// ($HOME via getenv, else the OS lookup). MCP clients store env values
// verbatim, so a literal "~" reaches app-store-connect-mcp unexpanded. "~user" forms and
// paths without a leading tilde are returned unchanged.
func ExpandHome(p string, getenv func(string) string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home := ""
	if getenv != nil {
		home = getenv("HOME")
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		home = h
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
