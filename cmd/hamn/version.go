package main

import (
	"runtime/debug"
	"strings"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// buildVersion returns the ldflags version, else the module version recorded
// by `go install`, else "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := moduleVersion(info.Main.Version); v != "" {
			return v
		}
	}
	return version
}

// moduleVersion maps a Go module version ("v0.1.3") to the ldflags form
// ("0.1.3"); "" for a missing or (devel) version.
func moduleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}
