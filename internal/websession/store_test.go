package websession

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func storeTestSession(n int) *Session {
	s := &Session{AppleID: "dev@example.com", ProviderID: 42, ProviderName: "Team", CreatedAt: time.Unix(1700000000, 0).UTC(), Source: "login"}
	s.Cookies = append(s.Cookies, Cookie{Name: "myacinfo", Value: "SECRET-MYAC", Domain: ".apple.com", Path: "/"})
	for i := range n {
		s.Cookies = append(s.Cookies, Cookie{Name: "c" + string(rune('a'+i%26)) + strings.Repeat("x", i), Value: strings.Repeat("v", 50), Domain: "appstoreconnect.apple.com"})
	}
	return s
}

func checkPerms(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(filepath.Join(dir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	files, _ := filepath.Glob(filepath.Join(dir, "web", "*.json"))
	if len(files) == 0 {
		t.Fatal("no session file")
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", f, fi.Mode().Perm())
		}
	}
}

func TestStoreForcedFileRoundTrip(t *testing.T) {
	keyring.MockInit()
	t.Setenv(EnvStore, "file")
	dir := t.TempDir()
	st := DefaultStore(dir)
	if _, err := st.Load("p"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("empty load: %v", err)
	}
	want := storeTestSession(1)
	if err := st.Save("p", want); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(KeychainService, "p"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("forced file must not touch the keychain: %v", err)
	}
	checkPerms(t, dir)
	got, err := st.Load("p")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppleID != want.AppleID || len(got.Cookies) != len(want.Cookies) || got.Cookies[0].Value != "SECRET-MYAC" {
		t.Fatalf("round trip: %v", got)
	}
	if err := st.Delete("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load("p"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after delete: %v", err)
	}
	if err := st.Delete("p"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestStoreKeychainThenSizeFallback(t *testing.T) {
	keyring.MockInit()
	t.Setenv(EnvStore, "")
	dir := t.TempDir()
	st := DefaultStore(dir)
	small := storeTestSession(1)
	if err := st.Save("p", small); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(KeychainService, "p"); err != nil {
		t.Fatalf("small session should be in the keychain: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "web", "*.json")); len(files) != 0 {
		t.Fatalf("unexpected files %v", files)
	}

	big := storeTestSession(60)
	if b, _ := json.Marshal(big); len(b) <= maxKeychainBytes {
		t.Fatalf("test session too small: %d", len(b))
	}
	if err := st.Save("p", big); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(KeychainService, "p"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("stale keychain copy left: %v", err)
	}
	checkPerms(t, dir)
	got, err := st.Load("p")
	if err != nil || len(got.Cookies) != len(big.Cookies) {
		t.Fatalf("load big: %v %v", got, err)
	}
	// Back to small: keychain again, file removed.
	if err := st.Save("p", small); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "web", "*.json")); len(files) != 0 {
		t.Fatalf("stale file left: %v", files)
	}
	if err := st.Delete("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load("p"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestStoreKeychainErrorFallsBackToFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	t.Cleanup(keyring.MockInit)
	t.Setenv(EnvStore, "")
	dir := t.TempDir()
	st := DefaultStore(dir)
	if err := st.Save("default", storeTestSession(1)); err != nil {
		t.Fatal(err)
	}
	checkPerms(t, dir)
	if _, err := st.Load("default"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("default"); err != nil {
		t.Fatalf("delete with unavailable keychain: %v", err)
	}
	if _, err := st.Load("default"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestStoreUnsafeProfileNameStaysInDir(t *testing.T) {
	t.Setenv(EnvStore, "file")
	dir := t.TempDir()
	st := DefaultStore(dir)
	if err := st.Save("../evil/x", storeTestSession(0)); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "web", "p-*.json"))
	if len(files) != 1 {
		t.Fatalf("hashed file missing: %v", files)
	}
	if _, err := st.Load("../evil/x"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCorruptFileHidesContent(t *testing.T) {
	t.Setenv(EnvStore, "file")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "p.json"), []byte(`{"cookies":"SECRET-X`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DefaultStore(dir).Load("p")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("corrupt load: %v", err)
	}
}
