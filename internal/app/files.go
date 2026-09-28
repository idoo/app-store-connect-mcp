package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// ErrOutsideRoot is returned for paths that resolve outside the confined root,
// including through symlinks.
var ErrOutsideRoot = errors.New("path is outside the allowed root")

// Files is the confined [tool.Files]. Every access goes through an [os.Root],
// so the kernel-level lookup cannot escape Root even through symlinks or a
// path swapped between check and use.
type Files struct {
	root string
	r    *os.Root
}

var _ tool.Files = (*Files)(nil)

// NewFiles confines file access to root, which must be an existing directory.
func NewFiles(root string) (*Files, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("files: root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("files: root %q: %w", root, err)
	}
	fi, err := os.Stat(resolved) //nolint:gosec // root is chosen by the user (--root/HAMN_ROOT, client roots or cwd); confinement starts here
	if err != nil {
		return nil, fmt.Errorf("files: root %q: %w", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("files: root %q is not a directory", root)
	}
	r, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: root %q: %w", root, err)
	}
	return &Files{root: resolved, r: r}, nil
}

// DefaultRoot returns the git root containing cwd (the nearest ancestor with a
// .git entry), or cwd itself when it is not inside a repository.
func DefaultRoot(cwd string) string {
	for dir := filepath.Clean(cwd); ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(cwd)
		}
		dir = parent
	}
}

// Root returns the confined root directory (symlinks resolved).
func (f *Files) Root() string { return f.root }

// Close releases the root directory handle. Windows refuses to delete or
// rename a directory while a handle to it is open.
func (f *Files) Close() error { return f.r.Close() }

// Resolve returns the absolute, symlink-free path for p, or an error wrapping
// [ErrOutsideRoot]. p may be relative to the root or absolute. Components that
// do not exist yet are allowed, so the result can be used for new files.
func (f *Files) Resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("files: empty path")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(f.root, abs)
	}
	abs = filepath.Clean(abs)
	// Resolve symlinks on the longest existing prefix; the rest is appended
	// verbatim (it cannot contain symlinks because it does not exist).
	existing, rest := abs, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("files: resolve %q: %w", p, err)
	}
	if rest != "" {
		resolved = filepath.Join(resolved, rest)
	}
	if !within(f.root, resolved) {
		return "", fmt.Errorf("files: %q: %w %s", p, ErrOutsideRoot, f.root)
	}
	return resolved, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// rel maps p to a path relative to the root for the [os.Root] methods. The
// lexical check here only produces the name; os.Root enforces confinement.
func (f *Files) rel(p string) (string, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(f.root, resolved)
	if err != nil {
		return "", fmt.Errorf("files: %q: %w", p, ErrOutsideRoot)
	}
	return rel, nil
}

// ReadFile reads a confined file.
func (f *Files) ReadFile(p string) ([]byte, error) {
	rel, err := f.rel(p)
	if err != nil {
		return nil, err
	}
	b, err := f.r.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return b, nil
}

// WriteFile writes data atomically (temp file + rename), creating parent
// directories. perm 0 means 0644.
func (f *Files) WriteFile(p string, data []byte, perm fs.FileMode) error {
	rel, err := f.rel(p)
	if err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(rel)
	if err := f.r.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("files: %w", err)
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	name := filepath.Join(dir, "."+filepath.Base(rel)+".tmp-"+hex.EncodeToString(suffix[:]))
	tmp, err := f.r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = f.r.Remove(name)
		return fmt.Errorf("files: write %q: %w", p, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = f.r.Remove(name)
		return fmt.Errorf("files: write %q: %w", p, err)
	}
	if err := f.r.Rename(name, rel); err != nil {
		_ = f.r.Remove(name)
		return fmt.Errorf("files: write %q: %w", p, err)
	}
	return nil
}

// Open opens a confined file for reading.
func (f *Files) Open(p string) (*os.File, error) {
	rel, err := f.rel(p)
	if err != nil {
		return nil, err
	}
	fh, err := f.r.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return fh, nil
}

// Stat stats a confined path (following symlinks inside the root).
func (f *Files) Stat(p string) (fs.FileInfo, error) {
	rel, err := f.rel(p)
	if err != nil {
		return nil, err
	}
	fi, err := f.r.Stat(rel)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return fi, nil
}

// WalkDir walks a confined directory like [fs.WalkDir] over the root's
// [os.Root.FS]; callback paths are absolute. Symlinks are reported but not
// followed.
func (f *Files) WalkDir(root string, fn fs.WalkDirFunc) error {
	rel, err := f.rel(root)
	if err != nil {
		return err
	}
	err = fs.WalkDir(f.r.FS(), filepath.ToSlash(rel), func(p string, d fs.DirEntry, err error) error {
		return fn(filepath.Join(f.root, filepath.FromSlash(path.Clean(p))), d, err)
	})
	if err != nil {
		return fmt.Errorf("files: walk %q: %w", root, err)
	}
	return nil
}
