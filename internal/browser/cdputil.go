package browser

import (
	"net/url"
	"strings"
	"time"
)

// parseDevToolsActivePort turns the profile's DevToolsActivePort file (port
// line, then browser websocket path) into a websocket URL.
func parseDevToolsActivePort(b []byte) (string, bool) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		return "", false
	}
	port, path := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if port == "" || port == "0" || strings.Trim(port, "0123456789") != "" || !strings.HasPrefix(path, "/devtools/browser/") {
		return "", false
	}
	return "ws://127.0.0.1:" + port + path, true
}

// cdpExpires converts a CDP cookie expiry (seconds since epoch, <=0 for
// session cookies) to a time.
func cdpExpires(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	whole := int64(sec)
	return time.Unix(whole, int64((sec-float64(whole))*1e9)).UTC()
}

// headfulUA strips the headless marker from a browser user agent.
func headfulUA(ua string) string {
	return strings.ReplaceAll(ua, "HeadlessChrome", "Chrome")
}

// pageLabel is a page URL safe for error messages: scheme, host and path,
// without query or fragment (they can carry one-time tokens).
func pageLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "a sign-in page"
	}
	return u.Scheme + "://" + u.Host + u.Path
}
