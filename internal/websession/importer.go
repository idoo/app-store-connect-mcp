package websession

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// Import formats returned by [Import].
const (
	FormatHeader   = "header"
	FormatCurl     = "curl"
	FormatFastlane = "fastlane"
	FormatJSON     = "json"
)

// importDomain is the domain given to cookies that carry none (Cookie
// headers and cURL commands copied from appstoreconnect.apple.com).
const importDomain = "appstoreconnect.apple.com"

// errNoMyacinfo is returned when the input has no Apple ID session cookie.
var errNoMyacinfo = errors.New("websession: import: no myacinfo cookie found; sign in at https://appstoreconnect.apple.com in the browser and copy the cookies (or cURL) of a request to appstoreconnect.apple.com again")

// Import parses cookies exported from a browser or fastlane and returns a
// session plus the detected format: a Cookie header or "name=value; ..."
// line, a browser "Copy as cURL" command, a FASTLANE_SESSION YAML cookie
// list, or a Cookie-Editor/EditThisCookie JSON array. Cookies for non-Apple
// domains are dropped; myacinfo is required. Errors never contain values.
func Import(data []byte) (*Session, string, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
	if text == "" {
		return nil, "", errors.New("websession: import: input is empty")
	}
	var (
		cookies []Cookie
		format  string
		err     error
	)
	switch {
	case strings.HasPrefix(text, "["):
		format = FormatJSON
		cookies, err = parseJSONCookies(text)
	case strings.Contains(text, "!ruby/object:HTTP::Cookie"):
		format = FormatFastlane
		cookies, err = parseFastlane(text)
	case strings.HasPrefix(text, "curl ") || strings.HasPrefix(text, "curl\t") || strings.HasPrefix(text, "curl\\"):
		format = FormatCurl
		cookies, err = parseCurl(text)
	default:
		format = FormatHeader
		cookies, err = parseCookieLine(text)
	}
	if err != nil {
		return nil, format, err
	}
	now := time.Now()
	var kept []Cookie
	for _, c := range cookies {
		if c.Name == "" || !isAppleDomain(c.Domain) {
			continue
		}
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			continue
		}
		kept = dedupe(kept, c)
	}
	has := false
	for _, c := range kept {
		if c.Name == "myacinfo" && c.Value != "" {
			has = true
		}
	}
	if !has {
		return nil, format, errNoMyacinfo
	}
	return &Session{Cookies: kept, Source: "import:" + format}, format, nil
}

// dedupe appends c, replacing an earlier cookie with the same identity.
func dedupe(list []Cookie, c Cookie) []Cookie {
	for i, o := range list {
		if o.Name == c.Name && normDomain(o.Domain) == normDomain(c.Domain) && cookiePath(o.Path) == cookiePath(c.Path) {
			list[i] = c
			return list
		}
	}
	return append(list, c)
}

// parseCookieLine parses "Cookie: a=b; c=d" or "a=b; c=d".
func parseCookieLine(text string) ([]Cookie, error) {
	line := strings.Join(strings.Fields(text), " ")
	if i := strings.IndexByte(line, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(line[:i]), "cookie") {
		line = line[i+1:]
	}
	var out []Cookie
	for part := range strings.SplitSeq(line, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t\"'") {
			return nil, errors.New("websession: import: input is not a Cookie header, cURL command, FASTLANE_SESSION or JSON cookie export")
		}
		out = append(out, Cookie{Name: name, Value: strings.TrimSpace(value), Domain: importDomain, Path: "/", Secure: true})
	}
	if len(out) == 0 {
		return nil, errors.New("websession: import: no cookies found")
	}
	return out, nil
}

// parseCurl extracts cookies from a bash/zsh "Copy as cURL" command:
// -H/--header 'cookie: …', -b/--cookie '…'.
func parseCurl(text string) ([]Cookie, error) {
	args, err := shellWords(text)
	if err != nil {
		return nil, err
	}
	var lines []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		var header, cookie string
		switch {
		case a == "-H" || a == "--header":
			header = next()
		case strings.HasPrefix(a, "--header="):
			header = strings.TrimPrefix(a, "--header=")
		case strings.HasPrefix(a, "-H") && len(a) > 2:
			header = a[2:]
		case a == "-b" || a == "--cookie":
			cookie = next()
		case strings.HasPrefix(a, "--cookie="):
			cookie = strings.TrimPrefix(a, "--cookie=")
		case strings.HasPrefix(a, "-b") && len(a) > 2:
			cookie = a[2:]
		default:
			continue
		}
		if header != "" {
			name, value, ok := strings.Cut(header, ":")
			if ok && strings.EqualFold(strings.TrimSpace(name), "cookie") {
				lines = append(lines, value)
			}
		}
		if cookie != "" {
			if !strings.Contains(cookie, "=") {
				return nil, errors.New("websession: import: cURL reads cookies from a file (-b FILE); paste the cookie values instead")
			}
			lines = append(lines, cookie)
		}
	}
	if len(lines) == 0 {
		return nil, errors.New("websession: import: the cURL command has no cookie header; copy it from a request to appstoreconnect.apple.com while signed in")
	}
	return parseCookieLine(strings.Join(lines, "; "))
}

// shellWords splits a bash/zsh command line: '…', "…", $'…', backslash
// escapes and backslash-newline continuations.
func shellWords(s string) ([]string, error) {
	var (
		words []string
		cur   strings.Builder
		in    bool
	)
	flush := func() {
		if in {
			words = append(words, cur.String())
			cur.Reset()
			in = false
		}
	}
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\\':
			if i+1 < len(r) {
				i++
				if r[i] == '\n' {
					continue
				}
				if r[i] == '\r' && i+1 < len(r) && r[i+1] == '\n' {
					i++
					continue
				}
				cur.WriteRune(r[i])
				in = true
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '\'':
			in = true
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return nil, errors.New("websession: import: unterminated quote in cURL command")
			}
			cur.WriteString(string(r[i+1 : j]))
			i = j
		case c == '$' && i+1 < len(r) && r[i+1] == '\'':
			in = true
			j := i + 2
			for ; j < len(r) && r[j] != '\''; j++ {
				if r[j] == '\\' && j+1 < len(r) {
					j++
					switch r[j] {
					case 'n':
						cur.WriteByte('\n')
					case 't':
						cur.WriteByte('\t')
					case 'r':
						cur.WriteByte('\r')
					default:
						cur.WriteRune(r[j])
					}
					continue
				}
				cur.WriteRune(r[j])
			}
			if j >= len(r) {
				return nil, errors.New("websession: import: unterminated quote in cURL command")
			}
			i = j
		case c == '"':
			in = true
			j := i + 1
			for ; j < len(r) && r[j] != '"'; j++ {
				if r[j] == '\\' && j+1 < len(r) && strings.ContainsRune("\"\\$`\n", r[j+1]) {
					j++
					if r[j] != '\n' {
						cur.WriteRune(r[j])
					}
					continue
				}
				cur.WriteRune(r[j])
			}
			if j >= len(r) {
				return nil, errors.New("websession: import: unterminated quote in cURL command")
			}
			i = j
		default:
			cur.WriteRune(c)
			in = true
		}
	}
	flush()
	return words, nil
}

// jsonCookie is one Cookie-Editor / EditThisCookie export entry.
type jsonCookie struct {
	Name           string   `json:"name"`
	Value          string   `json:"value"`
	Domain         string   `json:"domain"`
	Path           string   `json:"path"`
	ExpirationDate *float64 `json:"expirationDate"`
	Secure         bool     `json:"secure"`
	HTTPOnly       bool     `json:"httpOnly"`
}

func parseJSONCookies(text string) ([]Cookie, error) {
	var in []jsonCookie
	if err := json.Unmarshal([]byte(text), &in); err != nil {
		// Do not include err: json errors can quote input bytes.
		return nil, errors.New("websession: import: the JSON is not a cookie array (name, value, domain, …)")
	}
	out := make([]Cookie, 0, len(in))
	for _, j := range in {
		c := Cookie{Name: j.Name, Value: j.Value, Domain: j.Domain, Path: j.Path, Secure: j.Secure, HTTPOnly: j.HTTPOnly}
		if j.ExpirationDate != nil && *j.ExpirationDate > 0 && !math.IsInf(*j.ExpirationDate, 0) {
			sec, frac := math.Modf(*j.ExpirationDate)
			c.Expires = time.Unix(int64(sec), int64(frac*1e9)).UTC()
		}
		out = append(out, c)
	}
	return out, nil
}

// parseFastlane reads spaceship's FASTLANE_SESSION: a YAML list of
// `- !ruby/object:HTTP::Cookie` mappings.
func parseFastlane(text string) ([]Cookie, error) {
	if !strings.Contains(text, "\n") && strings.Contains(text, `\n`) {
		text = strings.ReplaceAll(text, `\n`, "\n")
	}
	var (
		out []Cookie
		cur *Cookie
	)
	finish := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(t, "- ") || t == "-" {
			finish()
			if strings.Contains(t, "HTTP::Cookie") {
				cur = &Cookie{}
			}
			continue
		}
		if cur == nil || t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, raw, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		val := yamlScalar(strings.TrimSpace(raw))
		switch strings.TrimSpace(key) {
		case "name":
			cur.Name = val
		case "value":
			cur.Value = val
		case "domain":
			cur.Domain = val
		case "path":
			cur.Path = val
		case "secure":
			cur.Secure = val == "true"
		case "httponly":
			cur.HTTPOnly = val == "true"
		case "expires":
			cur.Expires = parseYAMLTime(val)
		}
	}
	finish()
	if len(out) == 0 {
		return nil, errors.New("websession: import: FASTLANE_SESSION has no cookies")
	}
	for i := range out {
		if out[i].Domain == "" {
			out[i].Domain = "apple.com"
		}
	}
	return out, nil
}

// yamlScalar unquotes a plain, single- or double-quoted YAML scalar.
func yamlScalar(v string) string {
	switch {
	case v == "" || v == "~" || v == "null":
		return ""
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		if u, err := strconv.Unquote(v); err == nil {
			return u
		}
		return v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

func parseYAMLTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.999999999 Z", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
