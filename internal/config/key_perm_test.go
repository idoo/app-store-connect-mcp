package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestKeyFilePerm reproduces L1: a world-readable .p8 loaded silently and a
// world-writable one (swappable by another user) was used for signing.
func TestKeyFilePerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	fastlaneJSON, err := json.Marshal(map[string]string{"key_id": "K1", "issuer_id": "I", "key": keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		fastlane bool
		warn     bool
		refuse   bool
	}{
		{name: "0600 clean", mode: 0o600},
		{name: "0644 warns", mode: 0o644, warn: true},
		{name: "0666 refused", mode: 0o666, refuse: true},
		{name: "fastlane 0644 warns", mode: 0o644, fastlane: true, warn: true},
		{name: "fastlane 0660 refused", mode: 0o660, fastlane: true, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "home")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "AuthKey_K1.p8")
			data := keyPEM
			env := map[string]string{"HOME": home, EnvKeyID: "K1", EnvIssuerID: "I", EnvKeyPath: path}
			if tc.fastlane {
				path = filepath.Join(dir, "api_key.json")
				data = string(fastlaneJSON)
			}
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			ref := KeyRef{Source: KeyFromFile, Path: path, KeyID: "K1"}
			if tc.fastlane {
				ref.Source = KeyFromFastlaneJSON
			}
			_, err := ref.Read(func(string) string { return "" })
			if tc.refuse != (err != nil && strings.Contains(err.Error(), "writable by group or others")) {
				t.Fatalf("Read err = %v, refuse %v", err, tc.refuse)
			}
			w, err := ref.CheckPerm()
			if (err != nil) != tc.refuse || (len(w) > 0) != tc.warn {
				t.Fatalf("CheckPerm = %v, %v", w, err)
			}
			if tc.fastlane {
				return
			}
			r, err := Resolve(Options{Getenv: envMap(env), ConfigPath: filepath.Join(dir, "none.toml"), Cwd: dir, Home: home})
			if err != nil {
				t.Fatal(err)
			}
			warned := slices.ContainsFunc(r.Warnings, func(s string) bool { return strings.Contains(s, "readable by other users") })
			problem := slices.ContainsFunc(r.Profile.Problems, func(s string) bool { return strings.Contains(s, "writable by group or others") })
			if warned != tc.warn || problem != tc.refuse {
				t.Fatalf("warnings %v problems %v", r.Warnings, r.Profile.Problems)
			}
		})
	}
}
