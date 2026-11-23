// Package developer reaches the Apple Developer account site
// (developer.apple.com) with the human's web session: any services-account
// call, the account and contact pages, and the forms on them. Reads go
// straight through; every write is previewed and approved by a human, and is
// sent once. Hidden form values and key material never reach the agent.
package developer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	siteHost   = "developer.apple.com"
	siteOrigin = "https://" + siteHost + "/"
	apiPrefix  = "services-account/"
	// inlineMax is the largest decoded answer returned inline; bigger ones
	// go to an artifact, as asc_api_get does (below the 60 KB result cap).
	inlineMax = 48 << 10
	// idMax and valueMax bound a page's own identifiers (ids, names) and
	// values (hrefs, actions, option values) returned outside an untrusted
	// wrapper.
	idMax    = 120
	valueMax = 500
)

// siteErr adds the sign-in step to a missing or expired web session, as the
// web toolset's tools do.
func siteErr(err error) error {
	if errors.Is(err, tool.ErrWebSessionRequired) {
		return tool.WithNext(err, tool.NextStep{Tool: "web_login"})
	}
	return err
}

// cleanPathSegments accepts plain relative path segments only: each is one
// or more of A-Z a-z 0-9 . _ ~ - and is not a dot segment. Escapes, matrix
// parameters, queries, fragments, backslashes and white space are refused.
func cleanPathSegments(p, what string) error {
	if p == "" {
		return tool.InvalidArgs("%s is empty", what)
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return tool.InvalidArgs("%s has an empty or dot segment: %s", what, tool.SanitizeLine(p, 200))
		}
		for i := range len(seg) {
			if !segmentByte(seg[i]) {
				return tool.InvalidArgs("%s must be a plain path such as %s (letters, digits, . _ ~ - between slashes)", what, examplePath(what))
			}
		}
	}
	return nil
}

func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func segmentByte(c byte) bool {
	return alnum(c) || c == '.' || c == '_' || c == '~' || c == '-'
}

func examplePath(what string) string {
	if what == "page path" {
		return "account/resources/identifiers/list"
	}
	return "v1/bundleIds"
}

// cleanAPIPath makes path relative to services-account/.
func cleanAPIPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	p = strings.TrimPrefix(p, siteOrigin)
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, apiPrefix)
	if err := cleanPathSegments(p, "path"); err != nil {
		return "", err
	}
	return p, nil
}

// cleanPagePath makes a page path (or a full https://developer.apple.com
// URL) relative to the site, its first segment one of
// [tool.DevSitePageRoots], and splits off its query.
func cleanPagePath(raw string) (string, url.Values, error) {
	p := strings.TrimSpace(raw)
	q := url.Values{}
	if strings.Contains(p, "://") {
		u, err := url.Parse(p)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, siteHost) || u.User != nil {
			return "", nil, tool.InvalidArgs("only https://%s/ pages are served; got %s", siteHost, tool.SanitizeLine(p, 200))
		}
		p, q = u.Path, u.Query()
	} else if before, query, ok := strings.Cut(p, "?"); ok {
		parsed, err := url.ParseQuery(query)
		if err != nil {
			return "", nil, tool.InvalidArgs("page query: %v", err)
		}
		p, q = before, parsed
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if err := cleanPathSegments(p, "page path"); err != nil {
		return "", nil, err
	}
	if !inRoots(p) {
		return "", nil, tool.InvalidArgs("page path must start with %s/", strings.Join(tool.DevSitePageRoots, "/ or "))
	}
	return p, q, nil
}

func inRoots(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return slices.Contains(tool.DevSitePageRoots, first)
}

// stringList converts a JSON value of a string or a list of strings.
func stringList(name string, v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, tool.InvalidArgs("%s: every value must be a string", name)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, tool.InvalidArgs("%s must be a string or a list of strings", name)
}

// values converts an object of string or string-list values; nil stays nil.
func values(arg string, m map[string]any) (url.Values, error) {
	if m == nil {
		return nil, nil
	}
	out := url.Values{}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		vs, err := stringList(arg+"."+k, m[k])
		if err != nil {
			return nil, err
		}
		out[k] = vs
	}
	return out, nil
}

// decodeAnswer turns an answer body into JSON data with every string but a
// plain token marked untrusted, or text for anything else.
func decodeAnswer(a tool.DevSiteAnswer) (data any, text string, isJSON bool) {
	body := bytes.TrimSpace(a.Body)
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') && json.Valid(body) {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber() // ids and timestamps keep every digit
		if dec.Decode(&data) == nil {
			return markUntrusted(data), "", true
		}
	}
	return nil, string(a.Body), false
}

// markUntrusted wraps, in place, every string value of v as
// [tool.Untrusted] unless it is a plain token ([plainToken]). Whatever key
// it stands under, text with a space in it never reaches the agent as a
// plain string. Keys are left as they are.
func markUntrusted(v any) any {
	switch x := v.(type) {
	case string:
		if !plainToken(x) {
			return tool.Untrusted(x)
		}
	case map[string]any:
		for k, e := range x {
			x[k] = markUntrusted(e)
		}
	case []any:
		for i, e := range x {
			x[i] = markUntrusted(e)
		}
	}
	return v
}

// plainToken reports a string that cannot carry prose: 1 to 200 bytes of
// A-Z a-z 0-9 . _ : / + = @ % ~ - only (ids, enums, dates, URLs and emails
// without spaces).
func plainToken(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !segmentByte(c) && !strings.ContainsRune(":/+=@%", rune(c)) {
			return false
		}
	}
	return true
}

// cut shortens s to max runes, marking the cut.
func cut(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes]) + fmt.Sprintf("… (%d characters)", len(r))
}

// slug is a file-name-safe form of a path for artifact names.
func slug(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
