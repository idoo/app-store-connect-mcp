package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	dirMode         = 0o700
	fileMode        = 0o600
	cursorKind      = "cursors"
	legacyCursorsDB = "cursors.json"
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

// DefaultDir returns asc-mcp's state directory: %LocalAppData%\asc-mcp on Windows,
// otherwise $XDG_STATE_HOME/asc-mcp or ~/.local/state/asc-mcp.
func DefaultDir() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "asc-mcp"), nil
		}
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "asc-mcp"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("state: home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "asc-mcp"), nil
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

// Cursor returns the stored cursor for profile/app/name, or "". Values
// written by asc-mcp 0.1.0 into the shared cursors.json are still read when no
// per-key record exists yet.
func (s *Store) Cursor(profile, app, name string) (string, error) {
	var rec cursorRecord
	ok, err := s.Load(cursorKind, cursorFile(profile, app, name), &rec)
	if err != nil || ok {
		return rec.Value, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readLegacyCursors()
	if err != nil {
		return "", err
	}
	return m[cursorKey(profile, app, name)], nil
}

// SetCursor stores a cursor value for profile/app/name. Each cursor is its
// own atomically replaced file, so concurrent asc-mcp processes updating
// different cursors never overwrite each other.
func (s *Store) SetCursor(profile, app, name, value string) error {
	return s.Save(cursorKind, cursorFile(profile, app, name), cursorRecord{
		Profile: profile, App: app, Name: name, Value: value,
	})
}

// cursorRecord is the on-disk form of one cursor.
type cursorRecord struct {
	Profile string `json:"profile"`
	App     string `json:"app"`
	Name    string `json:"name"`
	Value   string `json:"value"`
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

// readLegacyCursors reads the cursors.json map written by asc-mcp 0.1.0.
func (s *Store) readLegacyCursors() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(filepath.Join(s.dir, legacyCursorsDB))
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

// cursorFile is the record key of a cursor: a hash, since profile, app and
// name may hold characters that are not valid in file names.
func cursorFile(profile, app, name string) string {
	sum := sha256.Sum256([]byte(cursorKey(profile, app, name)))
	return hex.EncodeToString(sum[:])
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
