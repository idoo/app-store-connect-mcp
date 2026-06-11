package asctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// placeholder kinds of the deterministic mapping.
const (
	kindID     = "id"
	kindUUID   = "uuid"
	kindEmail  = "email"
	kindURL    = "url"
	kindBundle = "bundle"
	kindText   = "text"
	kindCode   = "code"
)

var (
	emailRe   = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	bundleRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*(\.[A-Za-z0-9-]+){2,}$`)
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	numericRe = regexp.MustCompile(`^[0-9]{5,}$`)
	enumRe    = regexp.MustCompile(`^[A-Z0-9_]+$`)
	dateRe    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}([T ][0-9]{2}:[0-9]{2}(:[0-9]{2}(\.[0-9]+)?)?(Z|[+-][0-9]{2}:?[0-9]{2})?)?$`)
	localeRe  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][A-Za-z0-9]{1,3}){0,2}$`)
	versionRe = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,6}){0,3}$`)
	// safeTokenRe bounds what an allowlisted key may hold: one short token.
	safeTokenRe = regexp.MustCompile(`^[A-Za-z0-9_.:+-]{1,64}$`)
	// literalSegRe matches API path literals (v1, apps, inAppPurchasesV2);
	// any other path segment is treated as an id.
	literalSegRe = regexp.MustCompile(`^(v[0-9]+|[a-z][a-zA-Z0-9]*)$`)
	// safeKeys hold structural values kept verbatim when they are one
	// short token; everything not kept is replaced (default deny).
	safeKeys = setOf(
		"type", "locale", "platform", "primaryLocale", "version", "versionString",
		"cfBundleShortVersionString", "cfBundleVersion", "inAppPurchaseType", "productType",
		"profileState", "profileType", "certificateType", "contentRightsDeclaration",
		"releaseType", "currency", "territory", "minOsVersion", "processingState",
		"appStoreState", "appVersionState", "reviewType", "kind", "status", "state",
	)
	codeAttrs = setOf(
		"sku", "vendorNumber", "udid", "serialNumber", "seedId", "teamId", "productId",
		"customCode", "appleId", "identifier", "providerId",
	)
	secretAttrs = setOf(
		"certificateContent", "profileContent", "demoAccountPassword", "password", "secret",
		"sharedSecret", "csrContent", "privateKey",
	)
	bundleAttrs = setOf("bundleId", "bundleIdentifier", "buildBundleId")
	emailAttrs  = setOf("email", "contactEmail", "feedbackEmail", "supportEmail")
	// apiHosts keep their host (and become fake-server links); their paths
	// keep only literals, ids are mapped and the query is dropped.
	apiHosts = setOf("api.appstoreconnect.apple.com")
)

// filler pads free-text placeholders to the original length.
const filler = " lorem ipsum dolor sit amet"

// Sanitizer replaces identifying data in App Store Connect JSON responses
// with deterministic placeholders. One Sanitizer keeps its mapping across
// calls, so the same id, email or bundle id maps to the same placeholder in
// every document it processes. It is not safe for concurrent use.
type Sanitizer struct {
	maps   map[string]map[string]string
	counts map[string]int
}

// NewSanitizer returns a Sanitizer with an empty mapping.
func NewSanitizer() *Sanitizer {
	return &Sanitizer{maps: map[string]map[string]string{}, counts: map[string]int{}}
}

// Sanitize returns doc with a fresh [Sanitizer] applied.
func Sanitize(doc []byte) ([]byte, error) { return NewSanitizer().Sanitize(doc) }

// Sanitize rewrites one JSON document, default-deny for strings:
//   - "id" values and numeric strings of 5+ digits → id-0001…; UUIDs →
//     00000000-0000-4000-8000-000000000001…; the same mapping applies to
//     ids inside API URL paths;
//   - emails → user0001@example.com; bundle ids → com.example.app1…;
//   - codes (sku, udid, vendor number…) → CODE0001…; secrets → REDACTED;
//   - API links keep the host and path literals, map ids and drop the whole
//     query (links.next is dropped); other URLs →
//     https://example.invalid/u0001, keeping a trailing image template
//     like {w}x{h}bb.{f};
//   - kept verbatim: one short token under a structural key (type, locale,
//     platform, state…), enums (^[A-Z0-9_]+$), ISO dates, locale codes and
//     version numbers;
//   - every other string → "<key> 0001 lorem…" with the same rune length.
//
// Objects are walked in sorted key order, so output is deterministic. The
// result is indented JSON with a trailing newline.
func (s *Sanitizer) Sanitize(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	v = s.value("", v)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sanitize: %w", err)
	}
	return append(out, '\n'), nil
}

// value sanitizes v found under key.
func (s *Sanitizer) value(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		return s.object(key, x)
	case []any:
		for i := range x {
			x[i] = s.value(key, x[i])
		}
		return x
	case string:
		return s.str(key, x)
	default:
		return v
	}
}

// object sanitizes a JSON object in sorted key order.
func (s *Sanitizer) object(key string, m map[string]any) map[string]any {
	if key == "links" {
		delete(m, "next")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m[k] = s.value(k, m[k])
	}
	return m
}

// str sanitizes one string value found under key. It is default-deny: a
// value survives only when it is structural (allowlisted key holding one
// short token, an enum, ISO date, locale code or version number); every
// other string is mapped or replaced by a same-length placeholder.
func (s *Sanitizer) str(key, v string) string {
	if v == "" {
		return v
	}
	switch {
	case secretAttrs[key]:
		return "REDACTED"
	case uuidRe.MatchString(v):
		return s.mapped(kindUUID, strings.ToLower(v), func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) })
	case key == "id" || numericRe.MatchString(v):
		return s.mapped(kindID, v, func(n int) string { return fmt.Sprintf("id-%04d", n) })
	case emailAttrs[key] || emailRe.MatchString(v):
		return s.mapped(kindEmail, strings.ToLower(v), func(n int) string { return fmt.Sprintf("user%04d@example.com", n) })
	case strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://"):
		return s.url(v)
	case bundleAttrs[key] || (key == "identifier" && bundleRe.MatchString(v)):
		return s.mapped(kindBundle, v, func(n int) string { return fmt.Sprintf("com.example.app%d", n) })
	case codeAttrs[key]:
		return s.mapped(kindCode, v, func(n int) string { return fmt.Sprintf("CODE%04d", n) })
	case safeKeys[key] && safeTokenRe.MatchString(v),
		enumRe.MatchString(v), dateRe.MatchString(v), localeRe.MatchString(v), versionRe.MatchString(v):
		return v
	}
	return s.text(key, v)
}

// text returns a placeholder with v's rune length.
func (s *Sanitizer) text(key, v string) string {
	p := s.mapped(kindText, key+"\x00"+v, func(n int) string { return fmt.Sprintf("%s %04d", key, n) })
	want := utf8.RuneCountInString(v)
	r := []rune(p)
	for len(r) < want {
		r = append(r, []rune(filler)...)
	}
	r = r[:want]
	if r[want-1] == ' ' {
		r[want-1] = 'x'
	}
	return string(r)
}

// url sanitizes a URL value.
func (s *Sanitizer) url(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return s.mapped(kindURL, v, func(n int) string { return fmt.Sprintf("https://example.invalid/u%04d", n) })
	}
	if apiHosts[u.Hostname()] {
		segs := strings.Split(u.Path, "/")
		for i, seg := range segs {
			if seg == "" {
				continue
			}
			if m, ok := s.maps[kindID][seg]; ok {
				segs[i] = m
			} else if !literalSegRe.MatchString(seg) {
				segs[i] = s.str("id", seg)
			}
		}
		return (&url.URL{Scheme: "https", Host: u.Host, Path: strings.Join(segs, "/")}).String()
	}
	tail := ""
	if i := strings.LastIndex(u.Path, "/"); i >= 0 && strings.Contains(u.Path[i:], "{") {
		tail = u.Path[i:]
	}
	key := u.Scheme + "://" + u.Host + u.Path
	return s.mapped(kindURL, key, func(n int) string { return fmt.Sprintf("https://example.invalid/u%04d", n) }) + tail
}

// mapped returns the placeholder of v in kind, creating it with mk.
func (s *Sanitizer) mapped(kind, v string, mk func(int) string) string {
	m := s.maps[kind]
	if m == nil {
		m = map[string]string{}
		s.maps[kind] = m
	}
	if p, ok := m[v]; ok {
		return p
	}
	s.counts[kind]++
	p := mk(s.counts[kind])
	m[v] = p
	return p
}

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
