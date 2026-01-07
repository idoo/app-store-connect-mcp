package state

import (
	"os"
	"path/filepath"
	"testing"
)

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

	for _, p := range []string{filepath.Join(dir, cursorsDB), filepath.Join(dir, "uploads", "abc123.json")} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != fileMode {
			t.Errorf("%s mode = %v, want 0600", p, fi.Mode().Perm())
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != dirMode {
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
