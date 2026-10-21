package ascrules

import (
	"net/http"
	"testing"
	"time"
)

// TestParseRetryAfter (CF2+CF5): huge values clamp before multiplying so
// they fail fast instead of overflowing to a negative (zero) wait; 0,
// negatives and past dates are unusable and fall back to the backoff floor.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	over := MaxRetryAfter + time.Second
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"7", 7 * time.Second, true},
		{" 30 ", 30 * time.Second, true},
		{"31", over, true},
		{"9223372036854775807", over, true},
		{"99999999999999999999999", over, true},
		{"9223372036", over, true},
		{now.Add(20 * time.Second).Format(http.TimeFormat), 20 * time.Second, true},
		{now.Add(24 * time.Hour).Format(http.TimeFormat), over, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, false},
		{now.Format(http.TimeFormat), 0, false},
		{"0", 0, false},
		{"-1", 0, false},
		{"-9223372036854775808", 0, false},
		{"", 0, false},
		{"soon", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseRetryAfter(tt.in, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
