package tooltest

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// update is the -update flag shared by every package using AssertGolden.
// Test packages must not define their own "update" flag.
var update = flag.Bool("update", false, "rewrite golden files")

// Call runs tl with argsJSON ("" means {}) against env and returns the result.
func Call(t testing.TB, tl tool.Tool, env *tool.Env, argsJSON string) *tool.Result {
	t.Helper()
	return tl.Call(context.Background(), env, json.RawMessage(argsJSON))
}

// MustOK fails the test if res is an error result and returns res.
func MustOK(t testing.TB, res *tool.Result) *tool.Result {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error result: %s", JSON(t, res.Data))
	}
	return res
}

// MustFail fails the test unless res is an error result with the given
// error code (ErrorBody.Error, e.g. "writes_disabled") and returns its body.
func MustFail(t testing.TB, res *tool.Result, code string) tool.ErrorBody {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected error %q, got success: %s", code, JSON(t, res.Data))
	}
	body, ok := res.Data.(tool.ErrorBody)
	if !ok {
		t.Fatalf("error result data is %T, want tool.ErrorBody", res.Data)
	}
	if body.Error != code {
		t.Fatalf("error code = %q (%s), want %q", body.Error, body.Message, code)
	}
	return body
}

// JSON returns the compact JSON of v.
func JSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Decode re-marshals res.Data into T (e.g. map[string]any or a typed struct).
func Decode[T any](t testing.TB, res *tool.Result) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(JSON(t, res.Data)), &v); err != nil {
		t.Fatalf("decode result into %T: %v", v, err)
	}
	return v
}

// golden is the on-disk shape of a result.
type golden struct {
	IsError bool            `json:"is_error,omitempty"`
	Data    any             `json:"data,omitempty"`
	Text    string          `json:"text,omitempty"`
	Images  int             `json:"images,omitempty"`
	Next    []tool.NextStep `json:"next,omitempty"`
}

// AssertGolden compares v (a *tool.Result or any JSON-marshalable value) with
// the indented JSON at path; with -update it rewrites the file. Result
// metadata (profile, rate limit) is excluded so goldens stay stable.
func AssertGolden(t testing.TB, path string, v any) {
	t.Helper()
	if r, ok := v.(*tool.Result); ok {
		v = golden{IsError: r.IsError, Data: r.Data, Text: r.Text, Images: len(r.Images), Next: r.Next}
	}
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // golden path chosen by the test author
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("golden %s mismatch (-want +got):\n%s", path, diff)
	}
}
