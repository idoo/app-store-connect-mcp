package asc

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	jwt := "eyJhbGciOiJFUzI1NiIsImtpZCI6IkFCQyJ9.eyJpc3MiOiJ4IiwiYXVkIjoiYXBwc3RvcmVjb25uZWN0LXYxIn0.MEUCIQDx-_abc"
	pem := "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQg\n-----END PRIVATE KEY-----"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "jwt", in: "token " + jwt + " end", want: "token <redacted:jwt> end"},
		{name: "pem block", in: "key:\n" + pem + "\nok", want: "key:\n<redacted:pem>\nok"},
		{name: "pem json escaped", in: `{"k":"-----BEGIN EC PRIVATE KEY-----\nMIGT\n-----END EC PRIVATE KEY-----"}`, want: `{"k":"<redacted:pem>"}`},
		{name: "unterminated key", in: `"-----BEGIN PRIVATE KEY-----\nMIGTAgEA`, want: `"<redacted:pem>`},
		{name: "authorization header", in: "Authorization: Bearer abc.def.ghi", want: "Authorization: Bearer <redacted>"},
		{name: "authorization header with jwt", in: "Authorization: Bearer " + jwt, want: "Authorization: Bearer <redacted>"},
		{name: "authorization json", in: `{"Authorization":"Basic dXNlcjpwYXNz"}`, want: `{"Authorization":"Basic <redacted>"}`},
		{name: "bare bearer", in: "sent bearer abcdEFGH1234ijkl5678", want: "sent bearer <redacted>"},
		{
			name: "signed s3 url",
			in:   "see https://bucket.s3.amazonaws.com/a/b.png?X-Amz-Algorithm=AWS4&X-Amz-Signature=deadbeef now",
			want: "see https://bucket.s3.amazonaws.com/a/b.png?<redacted> now",
		},
		{
			name: "signed url json escaped",
			in:   `{"url":"https://cdn.example/f.gz?Expires=1\u0026Signature=abc\u0026Key-Pair-Id=K"}`,
			want: `{"url":"https://cdn.example/f.gz?<redacted>"}`,
		},
		{name: "token param", in: "https://x.example/p?id=1&token=s3cret", want: "https://x.example/p?<redacted>"},
		{name: "loose signature param", in: "X-Amz-Signature=deadbeef&a=1", want: "X-Amz-Signature=<redacted>&a=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact = %q, want %q", got, tt.want)
			}
			if got := string(RedactBytes([]byte(tt.in))); got != tt.want {
				t.Errorf("RedactBytes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedactNoFalsePositives(t *testing.T) {
	for _, s := range []string{
		"The token was invalid; check your API key in doctor.",
		"Authorization failed for this request",
		"He is the bearer of bad news and bearer responsibilities",
		"https://api.appstoreconnect.apple.com/v1/apps?limit=10&sort=name&filter[bundleId]=se.jvcr.hjul",
		"https://developer.apple.com/system-status/",
		"eyJ alone is not a JWT, nor is eyJa.b.c",
		"-----BEGIN nothing here",
		`{"signatureRequired":true,"expiresAt":"2026-10-25"}`,
		"",
	} {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want unchanged", s, got)
		}
		b := []byte(s)
		if got := RedactBytes(b); string(got) != s {
			t.Errorf("RedactBytes(%q) = %q", s, got)
		}
	}
}

func TestRedactBytesNoCopyWhenClean(t *testing.T) {
	b := []byte(strings.Repeat("plain text ", 10))
	if got := RedactBytes(b); &got[0] != &b[0] {
		t.Fatal("clean input was copied")
	}
}
