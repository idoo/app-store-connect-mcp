package asc

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/secrets"
)

// Redaction placeholders.
const (
	redactedJWT   = "<redacted:jwt>"
	redactedPEM   = "<redacted:pem>"
	redactedValue = "<redacted>"
	redactedQuery = "?<redacted>"
)

var (
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

// Redact removes credentials from s: JWTs, PEM blocks (also base64-encoded,
// and bare base64 private-key DER bodies), Authorization and
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
	if secrets.PEMBlock.Match(b) {
		b = secrets.PEMBlock.ReplaceAllLiteral(b, []byte(redactedPEM))
	}
	if secrets.PEMOpenKey.Match(b) {
		b = secrets.PEMOpenKey.ReplaceAllLiteral(b, []byte(redactedPEM))
	}
	if secrets.PEMBase64.Match(b) {
		b = secrets.PEMBase64.ReplaceAllLiteral(b, []byte(redactedPEM))
	}
	if secrets.DERKeyBase64.Match(b) {
		b = secrets.DERKeyBase64.ReplaceAllFunc(b, redactDERKey)
	}
	if secrets.JWT.Match(b) {
		b = secrets.JWT.ReplaceAllLiteral(b, []byte(redactedJWT))
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

// redactDERKey replaces a [secrets.DERKeyBase64] match whose decoded header
// is a private key, keeping the boundary byte before it. Certificates and
// provisioning profiles (also MII…) are left alone.
func redactDERKey(m []byte) []byte {
	sub := secrets.DERKeyBase64.FindSubmatch(m)
	if !secrets.IsPrivateKeyDERBase64(sub[2]) {
		return m
	}
	return append(append([]byte{}, sub[1]...), redactedPEM...)
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
