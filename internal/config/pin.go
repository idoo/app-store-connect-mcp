package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PinFileName is the per-project pin file.
const PinFileName = ".asc-mcp.toml"

// Pin is a per-project .asc-mcp.toml. It never holds key material and can only
// narrow permissions.
type Pin struct {
	Profile string `toml:"profile,omitempty"`
	// App is an app id or bundle id used as the default app.
	App string `toml:"app,omitempty"`
	// AllowWrites is honored only when false.
	AllowWrites *bool `toml:"allow_writes,omitempty"`
	// Path is the pin file's absolute path (not part of the file).
	Path string `toml:"-"`
}

// Dir returns the directory holding the pin.
func (p *Pin) Dir() string { return filepath.Dir(p.Path) }

// FindPin walks up from cwd looking for .asc-mcp.toml. It stops after the git
// root (a directory containing .git), at home (exclusive of anything above
// it) or at the filesystem root. It returns nil when no pin exists.
func FindPin(cwd, home string) (*Pin, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("find pin: %w", err)
	}
	for {
		pin, err := loadPin(filepath.Join(dir, PinFileName))
		if err != nil || pin != nil {
			return pin, err
		}
		if (home != "" && sameDir(dir, home)) || exists(filepath.Join(dir, ".git")) {
			return nil, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

func loadPin(path string) (*Pin, error) {
	data, _, err := readChecked(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pin: %w", err)
	}
	var pin Pin
	if err := decodeStrict(data, &pin); err != nil {
		return nil, fmt.Errorf("pin %s: only profile, app and allow_writes are allowed: %w", path, err)
	}
	pin.Path = path
	return &pin, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sameDir compares directories after resolving symlinks, so /var and
// /private/var (macOS) match.
func sameDir(a, b string) bool {
	return canonical(a) == canonical(b)
}

func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
