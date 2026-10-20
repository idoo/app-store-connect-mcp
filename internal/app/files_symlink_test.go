package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// TestFilesRefuseSymlink reproduces L3 for explicit paths: a link inside the
// root to the project's key is neither read nor opened.
func TestFilesRefuseSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AuthKey.p8"), []byte("-----BEGIN PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "AuthKey.p8"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f, err := NewFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.ReadFile("notes.txt"); !errors.Is(err, tool.ErrSymlink) {
		t.Errorf("ReadFile err = %v", err)
	}
	if _, err := f.Open(filepath.Join(f.Root(), "notes.txt")); !errors.Is(err, tool.ErrSymlink) {
		t.Errorf("Open err = %v", err)
	}
	if _, err := f.ReadFile("AuthKey.p8"); err != nil {
		t.Errorf("regular file: %v", err)
	}
}
