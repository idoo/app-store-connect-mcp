// Command docsgen writes docs/tools.json and docs/TOOLS.md from the tool
// registry. Run it from the module root: go run ./internal/tools/docsgen.
package main

//go:generate go run . -out ../../../docs

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "docs", "output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	j, md, err := Generate(context.Background())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), j, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "TOOLS.md"), md, 0o600)
}
