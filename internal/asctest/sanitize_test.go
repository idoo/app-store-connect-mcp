package asctest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
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
	for _, leak := range []string{"1234567890", "jvcr", "Ivan", "HJUL2024", "BikeFan99", "fan@example.org", "Love it", "mzstatic", "MIIF", "7A3F00", "cursor", "eyJvZmZzZXQiOjF9", "6808967650", "hunter2", "lake house", "Unlisted", "3F2504E0", "9X8Y7Z6W5V", "PROF77ABCD", "filter", "+46"} {
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

func TestSanitizeLeavesNoIdentifyingStrings(t *testing.T) {
	doc := `{"data":{"type":"appStoreReviewDetails","id":"r1","attributes":{
		"reviewNote":"Use account ivan, password in the drawer",
		"brandNewFreeText":"Secret roadmap item",
		"uuid":"3f2504e0-4f89-41d3-9d01-0305e82c3301",
		"teamId":"QWERTY1234",
		"platform":"IOS","appStoreState":"READY_FOR_SALE","locale":"sv",
		"createdDate":"2026-10-25","versionString":"2.0"},
		"links":{"self":"https://api.appstoreconnect.apple.com/v1/apps/6808967650/builds?filter%5Bapps%5D=6808967650\u0026limit=5",
		"related":"https://api.appstoreconnect.apple.com/v1/reviewDetails/XG5ZX8A3QQ"}}}`
	out, err := Sanitize([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"6808967650", "ivan", "drawer", "Secret roadmap", "3f2504e0", "QWERTY1234", "XG5ZX8A3QQ", "filter", "limit=5"} {
		if strings.Contains(strings.ToLower(string(out)), strings.ToLower(leak)) {
			t.Errorf("output leaks %q:\n%s", leak, out)
		}
	}
	for _, keep := range []string{`"IOS"`, `"READY_FOR_SALE"`, `"sv"`, `"2026-10-25"`, `"2.0"`, `"appStoreReviewDetails"`, "/v1/apps/id-0003/builds"} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("output lost structural value %s:\n%s", keep, out)
		}
	}
}

func TestSanitizeStr(t *testing.T) {
	tests := []struct{ key, in, want string }{
		{"uuid", "3F2504E0-4F89-41D3-9D01-0305E82C3301", "00000000-0000-4000-8000-000000000001"},
		{"anything", "6808967650", "id-0001"},
		{"reviewNote", "Call me", "reviewN"},
		{"unknownKey", "hello", "unkno"},
		{"state", "IN_REVIEW", "IN_REVIEW"},
		{"anything", "WAITING_FOR_REVIEW", "WAITING_FOR_REVIEW"},
		{"anything", "2026-10-25T10:00:00Z", "2026-10-25T10:00:00Z"},
		{"anything", "zh-Hans", "zh-Hans"},
		{"anything", "1.2.3", "1.2.3"},
		{"platform", "has space", "platformx"},
	}
	s := NewSanitizer()
	for _, tt := range tests {
		if got := s.str(tt.key, tt.in); got != tt.want {
			t.Errorf("str(%q, %q) = %q, want %q", tt.key, tt.in, got, tt.want)
		}
	}
}

func TestSanitizeRejectsInvalidJSON(t *testing.T) {
	if _, err := Sanitize([]byte(`{"data":`)); err == nil {
		t.Fatal("expected error")
	}
}
