package asc

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
)

// Redaction placeholders.
const (
	redactedJWT   = "<redacted:jwt>"
	redactedPEM   = "<redacted:pem>"
	redactedValue = "<redacted>"
	redactedQuery = "?<redacted>"
)

var (
	// pemBlock matches a complete PEM block, including JSON-escaped newlines.
	pemBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----[\s\S]*?-----END [A-Z0-9 ]+-----`)
	// pemOpenKey matches an unterminated private key (e.g. a cut log line).
	pemOpenKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[A-Za-z0-9+/=\s]|\\[nr])*`)
	jwtToken   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]*`)
	// authHeader matches "Authorization: <scheme> <credentials>" in header,
	// JSON and key=value forms.
	authHeader = regexp.MustCompile(`(?i)(\bauthorization["']?\s*[:=]\s*["']?(?:bearer\s+|basic\s+)?)[^\s"',;]+`)
	// bearerToken matches a bare "Bearer <token>".
	bearerToken = regexp.MustCompile(`(?i)\b(bearer\s+)([A-Za-z0-9._~+/=-]{16,})`)
	// anyURL matches http(s) URLs, including JSON-escaped "&" and "/".
	anyURL = regexp.MustCompile(`https?://(?:[^\s"'<>\\]|\\u0026|\\/)+`)
	// signedParam matches signing parameters outside a URL.
	signedParam = regexp.MustCompile(`(?i)\b(X-Amz-Signature|X-Amz-Credential|X-Amz-Security-Token|Key-Pair-Id)=[^&\s"'\\]+`)

	redactLetters = regexp.MustCompile(`^[A-Za-z]+$`)
)

// Redact removes credentials from s: JWTs, PEM blocks, Authorization and
// Bearer credentials, and the query string of signed URLs (X-Amz-*,
// Signature, Expires, Key-Pair-Id, Policy, token, sig), keeping their scheme,
// host and path. Text without secrets is returned unchanged.
func Redact(s string) string {
	b := RedactBytes([]byte(s))
	if bytes.Equal(b, []byte(s)) {
		return s
	}
	return string(b)
}

// RedactBytes is [Redact] for byte slices. It returns b itself when nothing
// needs redacting.
func RedactBytes(b []byte) []byte {
	if pemBlock.Match(b) {
		b = pemBlock.ReplaceAllLiteral(b, []byte(redactedPEM))
	}
	if pemOpenKey.Match(b) {
		b = pemOpenKey.ReplaceAllLiteral(b, []byte(redactedPEM))
	}
	if jwtToken.Match(b) {
		b = jwtToken.ReplaceAllLiteral(b, []byte(redactedJWT))
	}
	if authHeader.Match(b) {
		b = authHeader.ReplaceAll(b, []byte("${1}"+redactedValue))
	}
	if bearerToken.Match(b) {
		b = bearerToken.ReplaceAllFunc(b, redactBearer)
	}
	if anyURL.Match(b) {
		b = anyURL.ReplaceAllFunc(b, redactURL)
	}
	if signedParam.Match(b) {
		b = signedParam.ReplaceAll(b, []byte("${1}="+redactedValue))
	}
	return b
}

// redactBearer replaces a bearer credential unless it is a plain word
// ("bearer responsibilities") or already redacted.
func redactBearer(m []byte) []byte {
	sub := bearerToken.FindSubmatch(m)
	if redactLetters.Match(sub[2]) {
		return m
	}
	return append(append([]byte{}, sub[1]...), redactedValue...)
}

// redactURL drops the query and fragment of a URL carrying signing
// parameters.
func redactURL(m []byte) []byte {
	q := bytes.IndexByte(m, '?')
	if q < 0 || !signedQuery(string(m[q+1:])) {
		return m
	}
	return append(append([]byte{}, m[:q]...), redactedQuery...)
}

// signedQuery reports whether a raw query string contains a signing key.
func signedQuery(raw string) bool {
	raw = strings.ReplaceAll(raw, `\u0026`, "&")
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	for part := range strings.SplitSeq(raw, "&") {
		key, _, _ := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(key); err == nil {
			key = k
		}
		switch k := strings.ToLower(key); {
		case strings.HasPrefix(k, "x-amz-"), strings.HasPrefix(k, "x-goog-"):
			return true
		case k == "signature", k == "expires", k == "key-pair-id", k == "policy",
			k == "token", k == "access_token", k == "sig":
			return true
		}
	}
	return false
}
