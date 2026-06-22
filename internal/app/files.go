package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// ErrOutsideRoot is returned for paths that resolve outside the confined root,
// including through symlinks.
var ErrOutsideRoot = errors.New("path is outside the allowed root")

// Files is the confined [tool.Files]: every path, after resolving symlinks,
// must stay inside Root.
type Files struct {
	root string
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
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: root %q: %w", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("files: root %q is not a directory", root)
	}
	return &Files{root: resolved}, nil
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

// ReadFile reads a confined file.
func (f *Files) ReadFile(p string) ([]byte, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(resolved) //nolint:gosec // path confined by Resolve.
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return b, nil
}

// WriteFile writes data atomically (temp file + rename), creating parent
// directories. perm 0 means 0644.
func (f *Files) WriteFile(p string, data []byte, perm fs.FileMode) error {
	resolved, err := f.Resolve(p)
	if err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("files: %w", err)
	}
	// Re-check after creating directories: a racing symlink must not escape.
	if again, err := f.Resolve(resolved); err != nil || again != resolved {
		if err == nil {
			err = fmt.Errorf("files: %q: %w", p, ErrOutsideRoot)
		}
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(resolved)+".tmp-*")
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
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
		_ = os.Remove(name)
		return fmt.Errorf("files: write %q: %w", p, err)
	}
	if err := os.Rename(name, resolved); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("files: write %q: %w", p, err)
	}
	return nil
}

// Open opens a confined file for reading.
func (f *Files) Open(p string) (*os.File, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(resolved) //nolint:gosec // path confined by Resolve.
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return fh, nil
}

// Stat stats a confined path (following symlinks inside the root).
func (f *Files) Stat(p string) (fs.FileInfo, error) {
	resolved, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	return fi, nil
}

// WalkDir walks a confined directory like [filepath.WalkDir]; callback paths
// are absolute. Symlinks are reported but not followed; reading through them
// goes through Resolve again.
func (f *Files) WalkDir(root string, fn fs.WalkDirFunc) error {
	resolved, err := f.Resolve(root)
	if err != nil {
		return err
	}
	if err := filepath.WalkDir(resolved, fn); err != nil {
		return fmt.Errorf("files: walk %q: %w", root, err)
	}
	return nil
}
