package websession

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
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// KeychainService is the OS keychain service holding web sessions; the
	// account is the credentials profile name.
	KeychainService = "app-store-connect-mcp-web"
	// EnvStore set to "file" keeps sessions out of the keychain (tests,
	// headless machines).
	EnvStore = "ASC_MCP_WEB_STORE"
	// maxKeychainBytes keeps values under Windows Credential Manager's
	// 2560-byte cap; larger sessions go to the file.
	maxKeychainBytes = 2500

	webDirMode  = 0o700
	webFileMode = 0o600
)

var profileFileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`)

// fileStore keeps sessions in the keychain, falling back to a 0600 file
// <stateDir>/web/<profile>.json.
type fileStore struct {
	dir string
	// forceFile skips the keychain entirely.
	forceFile bool
}

// DefaultStore returns the session store: the OS keychain (service
// [KeychainService], account = profile), with a 0600 file under
// <stateDir>/web when the keychain fails or the session is too large for it.
// ASC_MCP_WEB_STORE=file forces the file.
func DefaultStore(stateDir string) Store {
	return &fileStore{
		dir:       filepath.Join(stateDir, "web"),
		forceFile: strings.EqualFold(strings.TrimSpace(os.Getenv(EnvStore)), "file"),
	}
}

// path returns the session file for profile.
func (s *fileStore) path(profile string) string {
	return filepath.Join(s.dir, safeProfileName(profile)+".json")
}

// safeProfileName returns profile when it is safe as a file name; other
// names are hashed so a profile can never escape its directory.
func safeProfileName(profile string) string {
	if profileFileRE.MatchString(profile) && !strings.Contains(profile, "..") {
		return profile
	}
	sum := sha256.Sum256([]byte(profile))
	return "p-" + hex.EncodeToString(sum[:8])
}

// Load implements Store: keychain first, then the file.
func (s *fileStore) Load(profile string) (*Session, error) {
	if !s.forceFile {
		v, err := keyring.Get(KeychainService, profile)
		if err == nil {
			return decodeSession(profile, []byte(v), "keychain")
		}
	}
	b, err := os.ReadFile(s.path(profile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("websession: read session for profile %s: %w", profile, err)
	}
	return decodeSession(profile, b, "file")
}

func decodeSession(profile string, b []byte, where string) (*Session, error) {
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		// json errors may quote input bytes; never include them.
		return nil, fmt.Errorf("websession: stored session for profile %s (%s) is corrupt; run `app-store-connect-mcp web logout` and sign in again", profile, where)
	}
	if len(sess.Cookies) == 0 {
		return nil, ErrNoSession
	}
	return &sess, nil
}

// Save implements Store. The copy on the other backend is removed so Load
// never returns a stale session.
func (s *fileStore) Save(profile string, sess *Session) error {
	if sess == nil {
		return errors.New("websession: save: nil session")
	}
	b, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("websession: encode session: %w", err)
	}
	if !s.forceFile && len(b) <= maxKeychainBytes {
		if err := keyring.Set(KeychainService, profile, string(b)); err == nil {
			if err := os.Remove(s.path(profile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("websession: remove old session file: %w", err)
			}
			return nil
		}
	}
	if err := s.writeFile(s.path(profile), b); err != nil {
		return err
	}
	if !s.forceFile {
		_ = keyring.Delete(KeychainService, profile) // may be unavailable; Load prefers it only when present
	}
	return nil
}

// Delete implements Store: removes both copies.
func (s *fileStore) Delete(profile string) error {
	if !s.forceFile {
		if err := keyring.Delete(KeychainService, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			// An unavailable keychain holds nothing; fail only if the entry is still readable.
			if _, gerr := keyring.Get(KeychainService, profile); gerr == nil {
				return fmt.Errorf("websession: delete keychain entry for profile %s: %w", profile, err)
			}
		}
	}
	if err := os.Remove(s.path(profile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("websession: delete session file: %w", err)
	}
	return nil
}

// writeFile atomically replaces p with b (mode 0600, directory 0700).
func (s *fileStore) writeFile(p string, b []byte) error {
	if err := os.MkdirAll(s.dir, webDirMode); err != nil {
		return fmt.Errorf("websession: create %s: %w", s.dir, err)
	}
	if err := os.Chmod(s.dir, webDirMode); err != nil {
		return fmt.Errorf("websession: secure %s: %w", s.dir, err)
	}
	f, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("websession: write session: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(webFileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("websession: write session: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("websession: write session: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("websession: write session: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("websession: write session: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("websession: write session: %w", err)
	}
	ok = true
	return nil
}
