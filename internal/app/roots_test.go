package app

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

// fileURI returns the file:// URI for p: file:///C:/x on Windows (the drive
// is part of the path, not the host), file:///x elsewhere.
func fileURI(p string) string {
	s := filepath.ToSlash(p)
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return (&url.URL{Scheme: "file", Path: s}).String()
}

// newProjectFiles is NewProjectFiles that closes its roots at cleanup, before
// the temp dirs go (Windows cannot delete a directory with an open handle).
func newProjectFiles(t *testing.T, explicit, cwd, home string) *ProjectFiles {
	t.Helper()
	pf, err := NewProjectFiles(explicit, cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pf.Close() })
	return pf
}

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
			pf := newProjectFiles(t, "", tc.cwd, home)
			_, err := pf.ReadFile("x.txt")
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
	pf := newProjectFiles(t, home, string(filepath.Separator), home)
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
	pf := newProjectFiles(t, "", home, home)
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
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file URIs; Windows strips the leading slash (file:///C:/x)")
	}
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

// A root that contains the home folder, a top-level folder or the disk root
// would expose every project (and ~/.ssh): refused; a folder inside home is
// fine.
func TestUnsafeRootRefusesHomeAncestors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	base := t.TempDir()
	home := mkdir(t, base, "Users", "ivan")
	parent := filepath.Dir(home)
	proj := mkdir(t, home, "code", "x")
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{"/", true},
		{"/Users", true},
		{"/home", true},
		{home, true},
		{parent, true},
		{base, true},
		{proj, false},
	} {
		if got := unsafeRoot(c.dir, home); got != c.want {
			t.Errorf("unsafeRoot(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
	if !unsafeRoot("/Users", "") {
		t.Error("a top-level folder must be refused without a home too")
	}
	pf := newProjectFiles(t, "", proj, home)
	for _, uri := range []string{fileURI(parent), fileURI(base)} {
		if _, err := pf.ForRoots([]string{uri}); !errors.Is(err, tool.ErrUnavailable) {
			t.Errorf("client root %s: err = %v, want unavailable", uri, err)
		}
	}
	if _, err := pf.ForRoots([]string{fileURI(proj)}); err != nil {
		t.Errorf("client root inside home: %v", err)
	}
}
