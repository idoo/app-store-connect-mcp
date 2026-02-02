package asctest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/idoo/hamn/internal/tool/tooltest"
)

func TestSanitizeGolden(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "sanitize", "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Sanitize(in)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	tooltest.AssertGolden(t, filepath.Join("testdata", "sanitize", "golden.json"), json.RawMessage(out))

	again, err := Sanitize(in)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("sanitize is not deterministic")
	}
	for _, leak := range []string{"1234567890", "jvcr", "Ivan", "HJUL2024", "BikeFan99", "fan@example.org", "Love it", "mzstatic", "MIIF", "7A3F00", "cursor", "eyJvZmZzZXQiOjF9"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Errorf("sanitized output still contains %q", leak)
		}
	}
}

func TestSanitizerMappingIsSharedAcrossDocuments(t *testing.T) {
	s := NewSanitizer()
	a, err := s.Sanitize([]byte(`{"data":{"type":"apps","id":"555","attributes":{"bundleId":"se.a.b"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Sanitize([]byte(`{"data":{"type":"builds","id":"999","relationships":{"app":{"data":{"type":"apps","id":"555"}}},"links":{"self":"https://api.appstoreconnect.apple.com/v1/builds/999"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(a), `"id-0001"`) || !strings.Contains(string(a), "com.example.app1") {
		t.Fatalf("first doc = %s", a)
	}
	if !strings.Contains(string(b), `"id-0002"`) || !strings.Contains(string(b), `"id": "id-0001"`) ||
		!strings.Contains(string(b), "https://api.appstoreconnect.apple.com/v1/builds/id-0002") {
		t.Fatalf("second doc = %s", b)
	}
}

func TestSanitizeTextKeepsLength(t *testing.T) {
	s := NewSanitizer()
	for _, v := range []string{"A", "Hi there", "Ünïcödé text with accents", strings.Repeat("long text ", 40)} {
		got := s.text("subtitle", v)
		if utf8.RuneCountInString(got) != utf8.RuneCountInString(v) || strings.HasSuffix(got, " ") {
			t.Errorf("text(%q) = %q: length %d, want %d", v, got, utf8.RuneCountInString(got), utf8.RuneCountInString(v))
		}
	}
}

func TestSanitizeRejectsInvalidJSON(t *testing.T) {
	if _, err := Sanitize([]byte(`{"data":`)); err == nil {
		t.Fatal("expected error")
	}
}
