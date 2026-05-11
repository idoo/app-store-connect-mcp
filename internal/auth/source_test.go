package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychainRoundTrip(t *testing.T) {
	keyring.MockInit()
	pemData := pemPKCS8(t, newP256(t))

	if err := StoreKey("KC1", []byte("not a key")); err == nil {
		t.Fatal("StoreKey accepted invalid key")
	}
	if err := StoreKey("KC1", pemData); err != nil {
		t.Fatalf("StoreKey: %v", err)
	}
	got, err := LoadKey("KC1")
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(pemData)) {
		t.Fatal("LoadKey returned different bytes")
	}
	if _, err := ResolveKey(context.Background(), "KC1", KeychainSource{KeyID: "KC1"}); err != nil {
		t.Fatalf("ResolveKey keychain: %v", err)
	}
	if err := DeleteKey("KC1"); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if _, err := LoadKey("KC1"); !errors.Is(err, ErrKeyNotInKeychain) {
		t.Fatalf("LoadKey after delete err = %v, want ErrKeyNotInKeychain", err)
	}
	if err := DeleteKey("KC1"); !errors.Is(err, ErrKeyNotInKeychain) {
		t.Fatalf("DeleteKey twice err = %v, want ErrKeyNotInKeychain", err)
	}
}

func TestResolveKeySources(t *testing.T) {
	pemData := pemPKCS8(t, newP256(t))
	path := filepath.Join(t.TempDir(), "AuthKey_K1.p8")
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		src     KeySource
		wantErr bool
	}{
		{"file", FileSource{Path: path}, false},
		{"missing file", FileSource{Path: filepath.Join(t.TempDir(), "nope.p8")}, true},
		{"inline pem", InlineSource{Data: string(pemData), Origin: "env ASC_PRIVATE_KEY"}, false},
		{"inline base64", InlineSource{Data: base64.StdEncoding.EncodeToString(pemData)}, false},
		{"inline garbage", InlineSource{Data: "nope"}, true},
		{"nil source", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := ResolveKey(context.Background(), "K1", tc.src)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && key.ID() != "K1" {
				t.Fatalf("ID = %q, want K1", key.ID())
			}
		})
	}
}

// TestRedaction asserts no key material or JWT appears in any formatting of
// key holders, tokens or errors.
func TestRedaction(t *testing.T) {
	pemData := pemPKCS8(t, newP256(t))
	b64 := base64.StdEncoding.EncodeToString(pemData)
	key, err := NewKey("K1", pemData)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(key, "issuer")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.Sign(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	ts := NewTokenSource(signer)
	badPEM := strings.Replace(string(pemData), "PRIVATE KEY", "EC PRIVATE KEY", 2)
	_, errParse := ResolveKey(context.Background(), "K1", InlineSource{Data: badPEM, Origin: "env ASC_PRIVATE_KEY"})
	if errParse == nil {
		t.Fatal("expected parse error for mislabeled PEM")
	}

	body := strings.Split(string(pemData), "\n")[1] // first base64 line of the key
	secrets := []string{body, b64[:40], tok.Value, strings.Split(tok.Value, ".")[2]}
	values := []any{key, *key, signer, tok, ts, InlineSource{Data: string(pemData)}, InlineSource{Data: b64, Origin: "env"}, errParse}
	for _, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(verb, v)
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("%s of %T leaks secret: %q", verb, v, out)
				}
			}
		}
	}
}
