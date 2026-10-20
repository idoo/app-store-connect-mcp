package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrSymlink is returned by [Files] ReadFile and Open when the named file is
// itself a symbolic link. A link inside a project (release_notes.txt →
// AuthKey.p8) would otherwise send the target's bytes to Apple or the model.
var ErrSymlink = errors.New("refusing to read a symbolic link")

// IsSymlink reports whether a [Files.WalkDir] entry is a symbolic link.
// Directory readers skip such entries and report them as unread files.
func IsSymlink(d fs.DirEntry) bool { return d.Type()&fs.ModeSymlink != 0 }

// RefuseSymlink returns an error wrapping [ErrSymlink] when the last element
// of p (relative to root unless absolute) is a symbolic link. Missing paths
// pass; the caller's open reports them.
func RefuseSymlink(root, p string) error {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	fi, err := os.Lstat(filepath.Clean(abs))
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s: %w", p, ErrSymlink)
	}
	return nil
}
