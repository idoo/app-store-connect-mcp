package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// listTools returns the advertised tools of a fresh server over an
// in-memory client.
func listTools(t *testing.T, deps mcpserver.Deps) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	srv, err := mcpserver.Build(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	var out []*mcp.Tool
	for tl, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tl)
	}
	return out
}

// TestInputSchemasHaveSingleTypes guards Gemini and other OpenAPI-subset
// clients: no input schema of any tool, in either mode, uses a type array
// or the anyOf/oneOf constructs they reject.
func TestInputSchemasHaveSingleTypes(t *testing.T) {
	for _, writes := range []bool{false, true} {
		tools := listTools(t, mcpserver.Deps{
			Profile:  tool.ProfileInfo{Name: "p", KeyKind: tool.KeyTeam, AllowWrites: writes},
			Toolsets: tool.AllToolsets,
		})
		if len(tools) < 20 {
			t.Fatalf("writes=%v: only %d tools listed", writes, len(tools))
		}
		for _, tl := range tools {
			b, err := json.Marshal(tl.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatal(err)
			}
			if path := badSchemaNode(v, "$"); path != "" {
				t.Errorf("writes=%v %s: %s in %s", writes, tl.Name, path, b)
			}
		}
	}
}

// badSchemaNode returns the path of the first type array, anyOf or oneOf.
func badSchemaNode(v any, path string) string {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["type"].([]any); ok {
			return path + ".type is an array"
		}
		for _, k := range []string{"anyOf", "oneOf"} {
			if _, ok := x[k]; ok {
				return path + " has " + k
			}
		}
		for k, c := range x {
			if p := badSchemaNode(c, path+"."+k); p != "" {
				return p
			}
		}
	case []any:
		for _, c := range x {
			if p := badSchemaNode(c, path+"[]"); p != "" {
				return p
			}
		}
	}
	return ""
}

// TestInstructionsReadPathIsListedReadOnly checks that the generic read path
// the instructions give is actually listed in the default read-only mode.
func TestInstructionsReadPathIsListedReadOnly(t *testing.T) {
	listed := map[string]bool{}
	for _, tl := range listTools(t, mcpserver.Deps{Profile: tool.ProfileInfo{Name: "p", KeyKind: tool.KeyTeam}}) {
		listed[tl.Name] = true
	}
	for _, name := range []string{"asc_api_search", "asc_api_describe", "asc_api_get", "toolsets"} {
		if !strings.Contains(mcpserver.Instructions, name) || !listed[name] {
			t.Errorf("%s: in instructions=%v, listed read-only=%v", name, strings.Contains(mcpserver.Instructions, name), listed[name])
		}
	}
}

var secretMarkers = regexp.MustCompile(`X-Amz-Signature|-----BEGIN|Bearer eyJ|/AuthKey_[A-Z0-9]+\.p8|eyJ[A-Za-z0-9_-]{10,}\.eyJ`)

// TestCommittedOutputsContainNoSecrets scans every golden file, every
// fixture and docs/tools.json: redaction must leave each one unchanged and
// no token, PEM block, signed URL or .p8 path may appear.
func TestCommittedOutputsContainNoSecrets(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(root, "docs", "tools.json")}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(p)
		if strings.Contains(rel, "/testdata/golden/") || strings.Contains(rel, "/testdata/fixtures/") || strings.Contains(rel, "/golden/") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("found only %d files to scan; walker broken?", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, f)
		if !bytes.Equal(asc.RedactBytes(b), b) {
			t.Errorf("%s: redaction would change this file", rel)
		}
		if m := secretMarkers.Find(b); m != nil {
			t.Errorf("%s: contains %q", rel, m)
		}
	}
}
