package tool

import (
	"fmt"
	"time"
)

// ParseInstant parses a user- or Apple-supplied instant: RFC 3339 (with or
// without fractional seconds) or a plain YYYY-MM-DD date, which means UTC
// midnight. Callers that want another zone for date-only input must say so.
// Failures wrap [ErrInvalidArgs].
func ParseInstant(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not an ISO date or RFC 3339 time", ErrInvalidArgs, s)
	}
	return t, nil
}
