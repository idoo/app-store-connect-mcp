// Command hamn is the App Store Connect MCP server.
package main

import (
	"fmt"
	"os"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] != "version" {
		fmt.Fprintf(os.Stderr, "hamn %s: unknown command %q\n", version, os.Args[1])
		os.Exit(2)
	}
	fmt.Println("hamn", version)
}
