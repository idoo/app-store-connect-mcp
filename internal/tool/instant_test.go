package tool_test

import (
	"errors"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func TestParseInstant(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026-10-26T10:11:12Z":          time.Date(2026, 10, 26, 10, 11, 12, 0, time.UTC),
		"2026-10-26T10:11:12.5Z":        time.Date(2026, 10, 26, 10, 11, 12, 5e8, time.UTC),
		"2026-10-26T10:11:12-07:00":     time.Date(2026, 10, 26, 17, 11, 12, 0, time.UTC),
		"2026-10-26":                    time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		"2026-10-26T10:11:12.123+00:00": time.Date(2026, 10, 26, 10, 11, 12, 123e6, time.UTC),
	} {
		got, err := tool.ParseInstant(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseInstant(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "yesterday", "2026-13-01", "2026-10-26 10:11:12", "26/10/2026"} {
		if _, err := tool.ParseInstant(in); !errors.Is(err, tool.ErrInvalidArgs) {
			t.Errorf("ParseInstant(%q) err = %v, want ErrInvalidArgs", in, err)
		}
	}
}
