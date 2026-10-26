package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Pasted key paths: Windows "Copy as path" quotes and macOS Terminal
// drag-and-drop backslash escapes resolve to the real file.
func TestPastedKeyPath(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("backslash is the path separator on Windows")
	}
	dir := filepath.Join(t.TempDir(), "my keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "AuthKey_ABC.p8")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(filepath.Dir(dir), `my\ keys`, "AuthKey_ABC.p8")
	for _, in := range []string{
		key,
		`"` + key + `"`,
		`'` + key + `'`,
		escaped + " ",
	} {
		got, err := pastedKeyPath(in)
		if err != nil || got != key {
			t.Errorf("pastedKeyPath(%q) = %q, %v; want %q", in, got, err, key)
		}
	}
	// A missing file keeps the literal path so its error names what was typed.
	missing := filepath.Join(filepath.Dir(dir), `no\ such.p8`)
	if got, err := pastedKeyPath(missing); err != nil || got != missing {
		t.Errorf("missing = %q, %v", got, err)
	}
	// Mismatched quotes are not stripped.
	if got, _ := pastedKeyPath(`"` + key); got == key {
		t.Errorf("mismatched quote stripped: %q", got)
	}
}
