package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesRename(t *testing.T) {
	f, base := newTestFiles(t)
	if err := f.WriteFile("secrets/pending-ab.txt", []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename("secrets/pending-ab.txt", "secrets/42.txt"); err != nil {
		t.Fatal(err)
	}
	if b, err := f.ReadFile("secrets/42.txt"); err != nil || string(b) != "s3cret" {
		t.Errorf("renamed = %q, %v", b, err)
	}
	if _, err := f.Stat("secrets/pending-ab.txt"); err == nil {
		t.Error("old path still exists")
	}
	if err := f.Rename("secrets/42.txt", filepath.Join(base, "out.txt")); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("rename outside root err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "out.txt")); err == nil {
		t.Error("file escaped the root")
	}
	if err := f.Rename("../x.txt", "y.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("rename from outside err = %v", err)
	}
}
