package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned when key material cannot be used. Messages never
// contain key bytes.
var (
	// ErrEmptyKey means the key source produced no data.
	ErrEmptyKey = errors.New("private key is empty")
	// ErrNoPEM means the data is neither a PEM block nor base64-encoded PEM.
	ErrNoPEM = errors.New("private key is not PEM (expected the .p8 file from App Store Connect, as PEM or base64 PEM)")
	// ErrEncryptedKey means the PEM block is password-protected.
	ErrEncryptedKey = errors.New("private key is encrypted; App Store Connect .p8 keys are unencrypted PKCS#8")
	// ErrNotECDSA means the key is not an elliptic-curve key (e.g. RSA or Ed25519).
	ErrNotECDSA = errors.New("private key is not an ECDSA key; App Store Connect requires an EC P-256 (.p8) key")
	// ErrNotP256 means the key is ECDSA on a curve other than P-256.
	ErrNotP256 = errors.New("private key is not on curve P-256; App Store Connect requires ES256")
	// ErrNoKeyID means a key was constructed without its App Store Connect key id.
	ErrNoKeyID = errors.New("key id is empty")
)

// Key is an App Store Connect API private key together with its key id.
// Its String and GoString methods never reveal the key material.
type Key struct {
	id   string
	priv *ecdsa.PrivateKey
}

// NewKey parses data (PEM or base64 PEM) and binds it to the key id keyID.
func NewKey(keyID string, data []byte) (*Key, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, ErrNoKeyID
	}
	priv, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("key %s: %w", keyID, err)
	}
	return &Key{id: keyID, priv: priv}, nil
}

// ID returns the App Store Connect key id (the JWT "kid").
func (k Key) ID() string { return k.id }

// String identifies the key by id only.
func (k Key) String() string { return "auth.Key{ID:" + k.id + ", private:REDACTED}" }

// GoString redacts the key for %#v.
func (k Key) GoString() string { return k.String() }

// ParsePrivateKey decodes an EC P-256 private key from PEM (PKCS#8 "PRIVATE
// KEY" or SEC1 "EC PRIVATE KEY") or from base64-encoded PEM.
func ParsePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, ErrEmptyKey
	}
	block, _ := pem.Decode(data)
	if block == nil {
		decoded, err := decodeBase64(data)
		if err != nil {
			return nil, ErrNoPEM
		}
		if block, _ = pem.Decode(bytes.TrimSpace(decoded)); block == nil {
			return nil, ErrNoPEM
		}
	}
	return parseBlock(block)
}

func parseBlock(block *pem.Block) (*ecdsa.PrivateKey, error) {
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("decode PKCS#8 private key: %w", err)
		}
		priv, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w (got %T)", ErrNotECDSA, parsed)
		}
		return checkCurve(priv)
	case "EC PRIVATE KEY":
		priv, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("decode SEC1 EC private key: %w", err)
		}
		return checkCurve(priv)
	case "ENCRYPTED PRIVATE KEY":
		return nil, ErrEncryptedKey
	case "RSA PRIVATE KEY":
		return nil, fmt.Errorf("%w (got RSA)", ErrNotECDSA)
	default:
		return nil, fmt.Errorf("%w (unexpected PEM block %q)", ErrNotECDSA, block.Type)
	}
}

func checkCurve(priv *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	if priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w (got %s)", ErrNotP256, priv.Curve.Params().Name)
	}
	return priv, nil
}

func decodeBase64(data []byte) ([]byte, error) {
	s := strings.Join(strings.Fields(string(data)), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if out, err := enc.DecodeString(s); err == nil {
			return out, nil
		}
	}
	return nil, ErrNoPEM
}
