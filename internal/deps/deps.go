//go:build tools

// Package deps pins modules that later slices use but that no package imports
// yet, so `go mod tidy` keeps them in go.mod. Delete an import once a real
// package uses it.
package deps

import (
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/zalando/go-keyring"
)
