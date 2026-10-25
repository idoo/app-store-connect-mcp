package state

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestCursorsSurviveConcurrentProcesses uses one Store per goroutine (as
// separate hamn processes would, with no shared mutex) and checks no
// cursor update is lost.
func TestCursorsSurviveConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	const procs, keys = 8, 10
	var wg sync.WaitGroup
	for p := range procs {
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			for k := range keys {
				if err := s.SetCursor("prof", fmt.Sprintf("app%d", p), fmt.Sprintf("k%d", k), "v"); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for p := range procs {
		for k := range keys {
			if got, err := s.Cursor("prof", fmt.Sprintf("app%d", p), fmt.Sprintf("k%d", k)); err != nil || got != "v" {
				t.Fatalf("app%d/k%d = %q, %v: update lost", p, k, got, err)
			}
		}
	}
}

func TestCursorReadsLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{` + fmt.Sprintf("%q", cursorKey("p", "app", "reviews")) + `:"old"}`
	if err := os.WriteFile(filepath.Join(dir, legacyCursorsDB), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Cursor("p", "app", "reviews"); err != nil || got != "old" {
		t.Fatalf("legacy cursor = %q, %v", got, err)
	}
	if err := s.SetCursor("p", "app", "reviews", "new"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Cursor("p", "app", "reviews"); got != "new" {
		t.Fatalf("cursor = %q, want new (record wins over legacy file)", got)
	}
}

// TestStoreFilesAre0600AndAtomic checks record and kind-dir modes and that a
// failed write leaves the previous record intact.
func TestStoreFilesAre0600AndAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions as a non-root user")
	}
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type rec struct{ V string }
	if err := s.Save("uploads", "k", rec{"old"}); err != nil {
		t.Fatal(err)
	}
	kindDir := filepath.Join(s.Dir(), "uploads")
	for p, want := range map[string]os.FileMode{kindDir: dirMode, filepath.Join(kindDir, "k.json"): fileMode} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	if err := os.Chmod(kindDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(kindDir, dirMode) })
	if err := s.Save("uploads", "k", rec{"new"}); err == nil {
		t.Fatal("save into a read-only dir succeeded")
	}
	var got rec
	if ok, err := s.Load("uploads", "k", &got); !ok || err != nil || got.V != "old" {
		t.Fatalf("after failed save: %v %v %+v, want old record intact", ok, err, got)
	}
	entries, err := os.ReadDir(kindDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestStoreRoundTripAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hamn")
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Cursor("p", "app", "reviews"); err != nil || got != "" {
		t.Fatalf("empty cursor = %q, %v", got, err)
	}
	for _, v := range []string{"c1", "c2"} {
		if err := s.SetCursor("p", "app", "reviews", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCursor("p", "other", "reviews", "x"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Cursor("p", "app", "reviews"); got != "c2" {
		t.Fatalf("cursor = %q, want c2", got)
	}

	type rec struct{ N int }
	if err := s.Save("uploads", "abc123", rec{N: 7}); err != nil {
		t.Fatal(err)
	}
	var r rec
	if ok, err := s.Load("uploads", "abc123", &r); !ok || err != nil || r.N != 7 {
		t.Fatalf("load = %v %v %+v", ok, err, r)
	}
	if ok, err := s.Load("uploads", "missing", &r); ok || err != nil {
		t.Fatalf("missing load = %v %v", ok, err)
	}

	posix := runtime.GOOS != "windows" // Windows reports 0666/0777 modes.
	for _, p := range []string{filepath.Join(dir, cursorKind, cursorFile("p", "app", "reviews")+".json"), filepath.Join(dir, "uploads", "abc123.json")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if posix && fi.Mode().Perm() != fileMode {
			t.Errorf("%s mode = %v, want 0600", p, fi.Mode().Perm())
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if posix && fi.Mode().Perm() != dirMode {
		t.Errorf("dir mode = %v, want 0700", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" && !e.IsDir() {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}

	if err := s.Delete("uploads", "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("uploads", "abc123"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestStoreRejectsBadNames(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ kind, key string }{
		{"uploads", "../x"},
		{"..", "x"},
		{"uploads", ""},
		{"a/b", "x"},
		{"uploads", ".."},
	}
	for _, tt := range tests {
		if err := s.Save(tt.kind, tt.key, 1); err == nil {
			t.Errorf("Save(%q, %q) succeeded", tt.kind, tt.key)
		}
	}
}

func TestDefaultDirXDG(t *testing.T) {
	if os.Getenv("LocalAppData") != "" {
		t.Skip("windows layout")
	}
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	d, err := DefaultDir()
	if err != nil || d != filepath.Join("/tmp/xdg", "hamn") {
		t.Fatalf("DefaultDir = %q, %v", d, err)
	}
}
