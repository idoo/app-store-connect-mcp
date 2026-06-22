package main

import "runtime/debug"

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// buildVersion returns the ldflags version, else the module version recorded
// by `go install`, else "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}
