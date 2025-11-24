package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"testing"
)

func pemPKCS8(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newP256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func TestParsePrivateKey(t *testing.T) {
	p256 := newP256(t)
	good := pemPKCS8(t, p256)
	sec1DER, err := x509.MarshalECPrivateKey(p256)
	if err != nil {
		t.Fatal(err)
	}
	sec1 := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1DER})
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := base64.StdEncoding.EncodeToString(good)

	tests := []struct {
		name    string
		data    []byte
		wantErr error
	}{
		{"pkcs8 pem", good, nil},
		{"pkcs8 pem with surrounding whitespace", append(append([]byte("\n  "), good...), '\n'), nil},
		{"sec1 pem", sec1, nil},
		{"base64 pem", []byte(wrapped), nil},
		{"base64 pem with line breaks", []byte(wrapped[:40] + "\n" + wrapped[40:]), nil},
		{"empty", []byte("   "), ErrEmptyKey},
		{"garbage", []byte("not a key at all"), ErrNoPEM},
		{"base64 of non-pem", []byte(base64.StdEncoding.EncodeToString([]byte("hello"))), ErrNoPEM},
		{"rsa pkcs8", pemPKCS8(t, rsaKey), ErrNotECDSA},
		{"rsa pkcs1", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}), ErrNotECDSA},
		{"ed25519", pemPKCS8(t, edKey), ErrNotECDSA},
		{"p384", pemPKCS8(t, p384), ErrNotP256},
		{"encrypted", pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1}}), ErrEncryptedKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePrivateKey(tc.data)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(p256) {
				t.Fatal("parsed key differs from generated key")
			}
		})
	}
}

func TestNewKeyRequiresID(t *testing.T) {
	if _, err := NewKey(" ", pemPKCS8(t, newP256(t))); !errors.Is(err, ErrNoKeyID) {
		t.Fatalf("err = %v, want ErrNoKeyID", err)
	}
}
