package app

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

func fileURI(p string) string { return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String() }

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	d := filepath.Join(parts...)
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestProjectFilesRefusesHomeAndDiskRoot(t *testing.T) {
	home := mkdir(t, t.TempDir(), "home")
	notGit := mkdir(t, home, "plain") // no .git anywhere → DefaultRoot is cwd
	tests := []struct {
		name, cwd string
		refused   bool
	}{
		{"home", home, true},
		{"disk_root", string(filepath.Separator), true},
		{"plain_subdir", notGit, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pf, err := NewProjectFiles("", tc.cwd, home)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pf.ReadFile("x.txt")
			if got := errors.Is(err, tool.ErrUnavailable); got != tc.refused {
				t.Fatalf("ReadFile err = %v, refused = %v, want %v", err, got, tc.refused)
			}
			if tc.refused {
				body := tool.BodyFor(err)
				if body.Error != "unavailable" || !strings.Contains(body.Message, "--root") || !strings.Contains(body.Message, "HAMN_ROOT") {
					t.Fatalf("refusal must name --root and HAMN_ROOT: %+v", body)
				}
				if pf.Root() != "" {
					t.Fatalf("Root() = %q for a refused root", pf.Root())
				}
			}
		})
	}
}

func TestProjectFilesExplicitRootWins(t *testing.T) {
	home := mkdir(t, t.TempDir(), "home")
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An explicit --root is honoured even when it is home, and beats client roots.
	pf, err := NewProjectFiles(home, string(filepath.Separator), home)
	if err != nil {
		t.Fatal(err)
	}
	f, err := pf.ForRoots([]string{fileURI(mkdir(t, t.TempDir(), "client"))})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := f.ReadFile("a.txt"); err != nil || string(b) != "hi" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := NewProjectFiles(filepath.Join(home, "missing"), home, home); err == nil {
		t.Fatal("missing --root accepted")
	}
}

func TestProjectFilesClientRoots(t *testing.T) {
	base := t.TempDir()
	home := mkdir(t, base, "home")
	proj := mkdir(t, home, "proj")
	if err := os.WriteFile(filepath.Join(proj, "p.txt"), []byte("proj"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// cwd is home (refused by default), but the client names a project root.
	pf, err := NewProjectFiles("", home, home)
	if err != nil {
		t.Fatal(err)
	}
	f, err := pf.ForRoots([]string{"https://example.com/x", fileURI(filepath.Join(base, "missing")), fileURI(proj)})
	if err != nil {
		t.Fatalf("ForRoots: %v", err)
	}
	if b, err := f.ReadFile("p.txt"); err != nil || string(b) != "proj" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := f.ReadFile(filepath.Join(home, "secret")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("read outside client root: %v", err)
	}
	again, err := pf.ForRoots([]string{fileURI(proj)})
	if err != nil || again != f {
		t.Fatalf("client root not cached: %v", err)
	}
	// A client root that is home itself is refused like the fallback.
	if _, err := pf.ForRoots([]string{fileURI(home)}); !errors.Is(err, tool.ErrUnavailable) {
		t.Fatalf("home client root: %v", err)
	}
	// No usable client root falls back to the (refused) default.
	if _, err := pf.ForRoots(nil); !errors.Is(err, tool.ErrUnavailable) {
		t.Fatalf("fallback: %v", err)
	}
}

func TestRootPath(t *testing.T) {
	tests := []struct {
		uri  string
		ok   bool
		want string
	}{
		{"file:///tmp/x", true, filepath.FromSlash("/tmp/x")},
		{"file://localhost/tmp/x", true, filepath.FromSlash("/tmp/x")},
		{"file://otherhost/tmp/x", false, ""},
		{"https://x/tmp", false, ""},
		{"file://", false, ""},
	}
	for _, tc := range tests {
		got, ok := rootPath(tc.uri)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("rootPath(%q) = %q, %v", tc.uri, got, ok)
		}
	}
}
