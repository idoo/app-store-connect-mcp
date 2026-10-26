package asctest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/spec"
)

var (
	opsOnce sync.Once
	opsByID map[string]spec.Op
	errOps  error
)

// ValidateFixture checks that body is a JSON:API response document fitting
// operation opID: a "data" object (or null) for single-resource responses,
// an array for collections, resource objects carrying "type" and "id", the
// spec's response type when the spec declares one, and well-formed
// "included" entries. An "errors" document is accepted when every error has
// a status. Violations fail the test.
func ValidateFixture(t testing.TB, opID string, body []byte) {
	t.Helper()
	op, ok := lookupOp(t, opID)
	if !ok {
		t.Errorf("asctest: fixture for unknown operation %q", opID)
		return
	}
	for _, p := range fixtureProblems(op, body) {
		t.Errorf("asctest: fixture for %s: %s", opID, p)
	}
}

// fixtureProblems lists the shape violations of body for op.
func fixtureProblems(op spec.Op, body []byte) []string {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return []string{"not a JSON object: " + err.Error()}
	}
	if raw, ok := doc["errors"]; ok {
		return errorsProblems(raw)
	}
	raw, ok := doc["data"]
	if !ok {
		return []string{`missing top-level "data"`}
	}
	var probs []string
	trimmed := bytes.TrimSpace(raw)
	switch {
	case op.RespMany:
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil || trimmed[0] != '[' {
			return []string{`"data" must be an array for a collection response`}
		}
		for i, it := range items {
			probs = append(probs, resourceProblems(fmt.Sprintf("data[%d]", i), it, op.RespType)...)
		}
	case bytes.Equal(trimmed, []byte("null")):
	default:
		probs = append(probs, resourceProblems("data", trimmed, op.RespType)...)
	}
	if inc, ok := doc["included"]; ok {
		var items []json.RawMessage
		if err := json.Unmarshal(inc, &items); err != nil {
			probs = append(probs, `"included" must be an array`)
		}
		for i, it := range items {
			probs = append(probs, resourceProblems(fmt.Sprintf("included[%d]", i), it, "")...)
		}
	}
	return probs
}

// resourceProblems checks one resource object (or identifier).
func resourceProblems(at string, raw json.RawMessage, wantType string) []string {
	var res struct {
		Type *string `json:"type"`
		ID   *string `json:"id"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return []string{at + " must be a resource object"}
	}
	var probs []string
	switch {
	case res.Type == nil || *res.Type == "":
		probs = append(probs, at+` lacks "type"`)
	case wantType != "" && *res.Type != wantType:
		probs = append(probs, fmt.Sprintf("%s has type %q, the spec says %q", at, *res.Type, wantType))
	}
	if res.ID == nil || *res.ID == "" {
		probs = append(probs, at+` lacks "id"`)
	}
	return probs
}

// errorsProblems checks a JSON:API errors array.
func errorsProblems(raw json.RawMessage) []string {
	var errs []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &errs); err != nil || len(errs) == 0 {
		return []string{`"errors" must be a non-empty array`}
	}
	var probs []string
	for i, e := range errs {
		if e.Status == "" {
			probs = append(probs, fmt.Sprintf(`errors[%d] lacks "status"`, i))
		}
	}
	return probs
}

// lookupOp returns the full index entry of opID, including the response
// type, which the spec package keeps internal. It decodes the index file
// the spec package embeds.
func lookupOp(t testing.TB, opID string) (spec.Op, bool) {
	t.Helper()
	root, err := findModuleRoot()
	if err != nil {
		t.Fatalf("asctest: %v", err)
	}
	opsOnce.Do(func() { opsByID, errOps = loadOps(filepath.Join(root, "internal", "spec", "index.json.gz")) })
	if errOps != nil {
		t.Fatalf("asctest: %v", errOps)
	}
	op, ok := opsByID[opID]
	return op, ok
}

func loadOps(path string) (map[string]spec.Op, error) {
	gz, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec ops: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("spec ops: %w", err)
	}
	defer func() { _ = zr.Close() }()
	var f spec.File
	if err := json.NewDecoder(io.LimitReader(zr, 64<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("spec ops: %w", err)
	}
	out := make(map[string]spec.Op, len(f.Ops))
	for _, op := range f.Ops {
		out[op.ID] = op
	}
	return out, nil
}

// findModuleRoot returns the directory holding go.mod, walking up from the
// working directory (a test's package directory).
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("module root: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("module root: no go.mod above the working directory")
		}
		dir = parent
	}
}
