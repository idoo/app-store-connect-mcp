package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

// KeychainService is the OS keychain service name under which hamn stores
// .p8 keys; the account is the App Store Connect key id.
const KeychainService = "hamn"

// ErrKeyNotInKeychain means no key is stored in the keychain for the key id.
var ErrKeyNotInKeychain = errors.New("key not found in keychain")

// KeySource yields the PEM (or base64 PEM) bytes of a private key.
// Implementations must redact key material in String.
type KeySource interface {
	// Load returns the raw key bytes.
	Load(ctx context.Context) ([]byte, error)
	fmt.Stringer
}

// FileSource reads the key from a .p8 file on disk.
type FileSource struct {
	// Path is the .p8 file path.
	Path string
}

// Load reads the file.
func (s FileSource) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	return data, nil
}

// String names the file, never its contents.
func (s FileSource) String() string { return "file " + s.Path }

// InlineSource holds key bytes given directly, e.g. from ASC_PRIVATE_KEY.
type InlineSource struct {
	// Data is PEM or base64 PEM.
	Data string
	// Origin describes where Data came from (e.g. "env ASC_PRIVATE_KEY").
	Origin string
}

// Load returns Data.
func (s InlineSource) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(s.Data), nil
}

// String names the origin only.
func (s InlineSource) String() string {
	if s.Origin == "" {
		return "inline key (REDACTED)"
	}
	return s.Origin + " (REDACTED)"
}

// GoString redacts Data for %#v.
func (s InlineSource) GoString() string { return "auth.InlineSource{" + s.String() + "}" }

// KeychainSource loads the key from the OS keychain (service [KeychainService],
// account KeyID).
type KeychainSource struct {
	// KeyID is the App Store Connect key id used as the keychain account.
	KeyID string
}

// Load reads the key from the keychain.
func (s KeychainSource) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return LoadKey(s.KeyID)
}

// String names the keychain entry.
func (s KeychainSource) String() string { return "keychain " + KeychainService + "/" + s.KeyID }

// ResolveKey loads bytes from src and parses them as the key keyID.
// Errors name the source but never include key bytes.
func ResolveKey(ctx context.Context, keyID string, src KeySource) (*Key, error) {
	if src == nil {
		return nil, errors.New("resolve key: no key source")
	}
	data, err := src.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load key from %s: %w", src, err)
	}
	key, err := NewKey(keyID, data)
	if err != nil {
		return nil, fmt.Errorf("parse key from %s: %w", src, err)
	}
	return key, nil
}

// StoreKey validates pemData as an App Store Connect key and saves it in the
// OS keychain under keyID.
func StoreKey(keyID string, pemData []byte) error {
	if _, err := NewKey(keyID, pemData); err != nil {
		return fmt.Errorf("store key in keychain: %w", err)
	}
	if err := keyring.Set(KeychainService, keyID, strings.TrimSpace(string(pemData))); err != nil {
		return fmt.Errorf("store key %s in keychain: %w", keyID, err)
	}
	return nil
}

// LoadKey returns the key bytes stored in the OS keychain under keyID.
func LoadKey(keyID string) ([]byte, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, ErrNoKeyID
	}
	s, err := keyring.Get(KeychainService, keyID)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("key %s: %w", keyID, ErrKeyNotInKeychain)
	}
	if err != nil {
		return nil, fmt.Errorf("load key %s from keychain: %w", keyID, err)
	}
	return []byte(s), nil
}

// DeleteKey removes the keychain entry for keyID.
func DeleteKey(keyID string) error {
	err := keyring.Delete(KeychainService, keyID)
	if errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("key %s: %w", keyID, ErrKeyNotInKeychain)
	}
	if err != nil {
		return fmt.Errorf("delete key %s from keychain: %w", keyID, err)
	}
	return nil
}
