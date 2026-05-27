package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"

	"github.com/idoo/hamn/internal/tool"
)

const (
	dirMode   = 0o700
	fileMode  = 0o600
	cursorsDB = "cursors.json"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// Store is the file-backed [tool.StateStore]. Records are JSON files written
// atomically with mode 0600 under a 0700 directory. It is safe for concurrent
// use within one process.
type Store struct {
	dir string
	mu  sync.Mutex
}

var _ tool.StateStore = (*Store)(nil)

// DefaultDir returns hamn's state directory: %LocalAppData%\hamn on Windows,
// otherwise $XDG_STATE_HOME/hamn or ~/.local/state/hamn.
func DefaultDir() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "hamn"), nil
		}
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "hamn"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("state: home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "hamn"), nil
}

// New returns a Store rooted at dir, creating it with mode 0700.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("state: create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the root directory.
func (s *Store) Dir() string { return s.dir }

// Cursor returns the stored cursor for profile/app/name, or "".
func (s *Store) Cursor(profile, app, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readCursors()
	if err != nil {
		return "", err
	}
	return m[cursorKey(profile, app, name)], nil
}

// SetCursor stores a cursor value for profile/app/name.
func (s *Store) SetCursor(profile, app, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readCursors()
	if err != nil {
		return err
	}
	m[cursorKey(profile, app, name)] = value
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("state: encode cursors: %w", err)
	}
	return writeAtomic(filepath.Join(s.dir, cursorsDB), b)
}

// Load decodes the record kind/key into v and reports whether it existed.
func (s *Store) Load(kind, key string, v any) (bool, error) {
	p, err := s.recordPath(kind, key)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(p) //nolint:gosec // path is confined by recordPath.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("state: read %s/%s: %w", kind, key, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("state: decode %s/%s: %w", kind, key, err)
	}
	return true, nil
}

// Save stores v as the record kind/key.
func (s *Store) Save(kind, key string, v any) error {
	p, err := s.recordPath(kind, key)
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("state: encode %s/%s: %w", kind, key, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), dirMode); err != nil {
		return fmt.Errorf("state: create %s: %w", kind, err)
	}
	return writeAtomic(p, b)
}

// Delete removes the record kind/key; a missing record is not an error.
func (s *Store) Delete(kind, key string) error {
	p, err := s.recordPath(kind, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("state: delete %s/%s: %w", kind, key, err)
	}
	return nil
}

func (s *Store) recordPath(kind, key string) (string, error) {
	if !nameRE.MatchString(kind) || !nameRE.MatchString(key) || key == "." || key == ".." {
		return "", fmt.Errorf("state: invalid record name %q/%q", kind, key)
	}
	return filepath.Join(s.dir, kind, key+".json"), nil
}

func (s *Store) readCursors() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(s.dir, cursorsDB))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read cursors: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("state: decode cursors: %w", err)
	}
	return m, nil
}

func cursorKey(profile, app, name string) string {
	b, _ := json.Marshal([]string{profile, app, name}) // []string always marshals.
	return string(b)
}

// writeAtomic writes b to a temp file in the same directory, syncs it and
// renames it over path, so readers never observe a partial file.
func writeAtomic(path string, b []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("state: chmod: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("state: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("state: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("state: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}
