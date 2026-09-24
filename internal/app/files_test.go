package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newTestFiles(t *testing.T) (*Files, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	return f, base
}

func TestFilesConfinement(t *testing.T) {
	f, base := newTestFiles(t)
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(f.Root(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, filepath.Join(f.Root(), "outdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.Root(), "sub"), filepath.Join(f.Root(), "insidelink")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"../secret.txt",
		filepath.Join(base, "secret.txt"),
		"sub/../../secret.txt",
		"link.txt",
		"outdir/secret.txt",
		"outdir/newfile.txt",
		"/etc/passwd",
	} {
		if p == "/etc/passwd" && runtime.GOOS == "windows" {
			continue // drive-less, so not absolute on Windows: it resolves inside the root.
		}
		if _, err := f.Resolve(p); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Resolve(%q) err = %v, want ErrOutsideRoot", p, err)
		}
		if _, err := f.ReadFile(p); err == nil {
			t.Errorf("ReadFile(%q) succeeded", p)
		}
		if err := f.WriteFile(p, []byte("x"), 0); err == nil {
			t.Errorf("WriteFile(%q) succeeded", p)
		}
	}
	for p, want := range map[string]string{
		".":                    f.Root(),
		"sub/new/file.txt":     filepath.Join(f.Root(), "sub", "new", "file.txt"),
		"insidelink/a.txt":     filepath.Join(f.Root(), "sub", "a.txt"),
		f.Root() + "/sub/b.md": filepath.Join(f.Root(), "sub", "b.md"),
	} {
		got, err := f.Resolve(p)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", p, got, err, want)
		}
	}
	if _, err := f.Resolve(""); err == nil {
		t.Error("empty path accepted")
	}
}

func TestFilesWriteReadWalk(t *testing.T) {
	f, _ := newTestFiles(t)
	if err := f.WriteFile("meta/en-US/name.txt", []byte("Hamn"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("meta/en-US/name.txt", []byte("Hamn 2"), 0); err != nil {
		t.Fatal(err)
	}
	b, err := f.ReadFile("meta/en-US/name.txt")
	if err != nil || string(b) != "Hamn 2" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	fi, err := f.Stat("meta/en-US/name.txt")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o644 { // Windows reports 0666.
		t.Errorf("perm = %v, want 0644", fi.Mode().Perm())
	}
	fh, err := f.Open("meta/en-US/name.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
	var seen []string
	err = f.WalkDir("meta", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !filepath.IsAbs(p) {
			t.Errorf("walk path %q not absolute", p)
		}
		if !d.IsDir() {
			seen = append(seen, filepath.Base(p))
		}
		return nil
	})
	if err != nil || len(seen) != 1 || seen[0] != "name.txt" {
		t.Errorf("walk = %v, %v (temp files must not remain)", seen, err)
	}
}

func TestDefaultRoot(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "repo", "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRoot(deep); got != deep {
		t.Errorf("no repo: DefaultRoot = %q, want %q", got, deep)
	}
	if err := os.Mkdir(filepath.Join(base, "repo", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got, want := DefaultRoot(deep), filepath.Join(base, "repo"); got != want {
		t.Errorf("DefaultRoot = %q, want %q", got, want)
	}
}

func TestNewFilesRejectsNonDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFiles(p); err == nil {
		t.Error("want error for file root")
	}
}
