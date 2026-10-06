package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySource says where a private key's PEM lives.
type KeySource string

// Key sources.
const (
	// KeyFromFile reads a .p8 file at KeyRef.Path.
	KeyFromFile KeySource = "file"
	// KeyFromKeychain reads the OS keychain (service "asc-mcp", account = key id); internal/auth owns it.
	KeyFromKeychain KeySource = "keychain"
	// KeyFromEnv reads PEM or base64 PEM from the environment variable KeyRef.EnvVar.
	KeyFromEnv KeySource = "env"
	// KeyFromFastlaneJSON reads the inline "key" of a fastlane api_key.json at KeyRef.Path.
	KeyFromFastlaneJSON KeySource = "fastlane_json"
)

// ErrKeychainSource is returned by KeyRef.Read for keychain keys, which
// internal/auth reads itself.
var ErrKeychainSource = errors.New("key is stored in the keychain; read it via internal/auth")

// KeyRef locates a private key without holding its bytes, so config values
// are always safe to print.
type KeyRef struct {
	Source KeySource `json:"source"`
	// Path is the file for KeyFromFile and KeyFromFastlaneJSON.
	Path string `json:"path,omitempty"`
	// EnvVar names the variable for KeyFromEnv.
	EnvVar string `json:"env_var,omitempty"`
	// KeyID is the key's id (the keychain account).
	KeyID string `json:"key_id,omitempty"`
}

// String describes the location, never the key.
func (r KeyRef) String() string {
	switch r.Source {
	case KeyFromFile, KeyFromFastlaneJSON:
		return string(r.Source) + ":" + r.Path
	case KeyFromEnv:
		return "env:" + r.EnvVar
	case KeyFromKeychain:
		return "keychain:" + r.KeyID
	default:
		return "none"
	}
}

// Read returns the PEM bytes of the key. getenv is used for KeyFromEnv.
// Callers parse the key immediately and drop the bytes.
func (r KeyRef) Read(getenv func(string) string) ([]byte, error) {
	switch r.Source {
	case KeyFromFile:
		b, err := os.ReadFile(filepath.Clean(r.Path))
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		return normalizePEM(b, "file "+r.Path)
	case KeyFromEnv:
		v := getenv(r.EnvVar)
		if v == "" {
			return nil, fmt.Errorf("read private key: %s is empty", r.EnvVar)
		}
		return normalizePEM([]byte(v), r.EnvVar)
	case KeyFromFastlaneJSON:
		f, err := readFastlaneJSON(r.Path)
		if err != nil {
			return nil, err
		}
		if f.Key == "" {
			return nil, fmt.Errorf("read private key: %s has no \"key\"", r.Path)
		}
		return normalizePEM([]byte(f.Key), r.Path)
	case KeyFromKeychain:
		return nil, ErrKeychainSource
	default:
		return nil, errors.New("read private key: no key source configured")
	}
}

var pemBegin = []byte("-----BEGIN ")

// normalizePEM accepts PEM, PEM with literal \n escapes, or base64 PEM.
// what names the origin for errors; the key itself never appears in errors.
func normalizePEM(b []byte, what string) ([]byte, error) {
	b = bytes.TrimSpace(b)
	if !bytes.Contains(b, pemBegin) {
		compact := bytes.Join(bytes.Fields(b), nil)
		dec := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
		n, err := base64.StdEncoding.Decode(dec, compact)
		if err != nil || !bytes.Contains(dec[:n], pemBegin) {
			return nil, fmt.Errorf("private key from %s is neither PEM nor base64 PEM", what)
		}
		b = bytes.TrimSpace(dec[:n])
	}
	if !bytes.Contains(b, []byte("\n")) && bytes.Contains(b, []byte(`\n`)) {
		b = bytes.ReplaceAll(b, []byte(`\n`), []byte("\n"))
	}
	return b, nil
}

// fastlaneKey is fastlane's api_key.json format.
type fastlaneKey struct {
	KeyID       string `json:"key_id"`
	IssuerID    string `json:"issuer_id"`
	Key         string `json:"key"`
	KeyFilepath string `json:"key_filepath"`
}

func readFastlaneJSON(path string) (*fastlaneKey, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read fastlane api key: %w", err)
	}
	var f fastlaneKey
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse fastlane api key %s: %w", path, err)
	}
	f.KeyID = strings.TrimSpace(f.KeyID)
	f.IssuerID = strings.TrimSpace(f.IssuerID)
	return &f, nil
}
