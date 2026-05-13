package asc

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidCursor reports a next_cursor that is malformed or points outside
// the collection it was issued for.
var ErrInvalidCursor = errors.New("asc: invalid cursor")

// maxCursorLen bounds the work done on attacker-supplied cursors.
const maxCursorLen = 8 << 10

// pageQueryKey matches the query keys App Store Connect uses on collection
// URLs; anything else in a cursor or next link is rejected.
var pageQueryKey = regexp.MustCompile(`^(?:include|limit|sort|cursor|(?:filter|fields|limit|exists)\[[A-Za-z0-9_.]+\])$`)

// EncodeCursor returns the opaque next_cursor for an absolute next-page URL.
func EncodeCursor(nextURL string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(nextURL))
}

// DecodeCursor decodes a cursor produced by [EncodeCursor] and validates the
// URL it carries against base and wantPath: https (or base's scheme when base
// is a loopback test server), the same host and port as base, no userinfo or
// fragment, path exactly wantPath, and only collection query keys (filter[…],
// fields[…], include, limit, limit[…], sort, exists[…], cursor). It returns
// the canonical URL; its host is always base.Host. Errors wrap
// [ErrInvalidCursor].
func DecodeCursor(cursor string, base *url.URL, wantPath string) (string, error) {
	if cursor == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidCursor)
	}
	if len(cursor) > maxCursorLen {
		return "", fmt.Errorf("%w: longer than %d bytes", ErrInvalidCursor, maxCursorLen)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cursor, "="))
	if err != nil {
		return "", fmt.Errorf("%w: not base64url: %w", ErrInvalidCursor, err)
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	s, err := checkPageURL(u, base, wantPath)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	return s, nil
}

// checkPageURL enforces the cursor URL policy on u and returns its canonical
// form (base's host spelling, sorted and re-encoded query).
func checkPageURL(u, base *url.URL, wantPath string) (string, error) {
	if base == nil || base.Host == "" {
		return "", errors.New("no base URL")
	}
	if !u.IsAbs() || u.Opaque != "" {
		return "", errors.New("not an absolute hierarchical URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == base.Scheme && cursorLoopback(base):
	default:
		return "", fmt.Errorf("scheme %q not allowed", u.Scheme)
	}
	if u.User != nil {
		return "", errors.New("userinfo not allowed")
	}
	if !strings.EqualFold(u.Host, base.Host) {
		return "", fmt.Errorf("host %q differs from %q", u.Host, base.Host)
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return "", errors.New("fragment not allowed")
	}
	if u.RawPath != "" || u.Path != wantPath {
		return "", fmt.Errorf("path %q differs from %q", u.EscapedPath(), wantPath)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("bad query: %w", err)
	}
	for k := range q {
		if !pageQueryKey.MatchString(k) {
			return "", fmt.Errorf("query key %q not allowed", k)
		}
	}
	c := url.URL{Scheme: u.Scheme, Host: base.Host, Path: u.Path, RawQuery: q.Encode()}
	return c.String(), nil
}

// cursorLoopback reports whether base points at a loopback test server.
func cursorLoopback(base *url.URL) bool {
	h := base.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
