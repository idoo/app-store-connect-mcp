// Package secrets holds the private-key and token patterns shared by the
// redactor ([asc.Redact]) and the approval-prompt warning
// ([tool.LooksLikeSecret]), so the two can never disagree about what a key
// looks like. It is a leaf package: it imports only the standard library.
package secrets

import (
	"bytes"
	"encoding/base64"
	"regexp"
)

var (
	// PEMBlock matches a complete PEM block of any type (keys, certificates),
	// including JSON-escaped newlines. It never crosses an unescaped `"`, so a
	// match stays inside one JSON string and cannot swallow neighbouring
	// values.
	PEMBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----(?:[^"\\]|\\.)*?-----END [A-Z0-9 ]+-----`)
	// PEMOpenKey matches a private-key PEM block from its BEGIN line, also
	// unterminated (e.g. a cut log line). Certificates never match.
	PEMOpenKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[A-Za-z0-9+/=\s]|\\[nr])*`)
	// JWT matches a JWT (base64url JSON header and claims, optional signature).
	JWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{2,}\.eyJ[A-Za-z0-9_-]{2,}\.[A-Za-z0-9_-]*`)
	// PEMBase64 matches base64 whose plaintext starts with "-----BEGIN" (the
	// base64 form of ASC_PRIVATE_KEY, or of a certificate). Like PEMBlock it
	// never crosses a `"`.
	PEMBase64 = regexp.MustCompile(`LS0tLS1CRUdJT(?:[A-Za-z0-9+/]|\\[nr]|\r?\n)*={0,2}`)
	// DERKeyBase64 matches a bare base64 DER body (a .p8 without its PEM
	// lines) starting at a base64 boundary: MIG/MII are SEQUENCEs with long
	// lengths, MHc a P-256 SEC1 key. Submatch 1 is the boundary byte, 2 the
	// body. Certificates and provisioning profiles match too;
	// [IsPrivateKeyDERBase64] decides on the header.
	DERKeyBase64 = regexp.MustCompile(`(^|[^A-Za-z0-9+/])((?:MI[GI]|MHc)(?:[A-Za-z0-9+/]|\\[nr]|\r?\n){97,}={0,2})`)
)

// IsPrivateKeyDER reports whether der starts like a PKCS#8 or PKCS#1 key
// (SEQUENCE, INTEGER version 0) or a SEC1 EC key (SEQUENCE, INTEGER 1,
// OCTET STRING). Certificates and CMS start SEQUENCE, SEQUENCE/OID instead.
func IsPrivateKeyDER(der []byte) bool {
	if len(der) < 2 || der[0] != 0x30 {
		return false
	}
	hdr := 2
	switch der[1] {
	case 0x81:
		hdr = 3
	case 0x82:
		hdr = 4
	}
	if len(der) < hdr {
		return false
	}
	v := der[hdr:]
	return len(v) >= 4 && v[0] == 0x02 && v[1] == 0x01 && (v[2] == 0x00 || (v[2] == 0x01 && v[3] == 0x04))
}

// IsPrivateKeyDERBase64 reports whether body, a [DERKeyBase64] body (submatch
// 2), decodes to a private-key header per [IsPrivateKeyDER].
func IsPrivateKeyDERBase64(body []byte) bool {
	der, ok := decodePrefix(body, 16)
	return ok && IsPrivateKeyDER(der)
}

// IsPrivateKeyPEMBase64 reports whether m, a [PEMBase64] match, is the
// base64 of a private-key PEM block rather than of a certificate.
func IsPrivateKeyPEMBase64(m []byte) bool {
	pem, ok := decodePrefix(m, 64)
	return ok && bytes.Contains(pem, []byte("PRIVATE KEY-----"))
}

// ContainsPrivateKeyOrToken reports whether s contains a private key (PEM,
// base64 PEM or a bare base64 DER body) or a JWT. Certificates and
// provisioning profiles, which tools legitimately read and return, do not
// count. It is a detector; use [asc.Redact] to remove secrets.
func ContainsPrivateKeyOrToken(s string) bool {
	if JWT.MatchString(s) || PEMOpenKey.MatchString(s) {
		return true
	}
	b := []byte(s)
	for _, m := range PEMBase64.FindAll(b, -1) {
		if IsPrivateKeyPEMBase64(m) {
			return true
		}
	}
	for _, sub := range DERKeyBase64.FindAllSubmatch(b, -1) {
		if IsPrivateKeyDERBase64(sub[2]) {
			return true
		}
	}
	return false
}

// decodePrefix base64-decodes the first n base64 characters of m, skipping
// raw and JSON-escaped line breaks. n must be a multiple of 4.
func decodePrefix(m []byte, n int) ([]byte, bool) {
	clean := make([]byte, 0, n)
	for i := 0; i < len(m) && len(clean) < n; i++ {
		switch c := m[i]; c {
		case '\\':
			i++ // escaped \n or \r
		case '\n', '\r':
		default:
			clean = append(clean, c)
		}
	}
	clean = clean[:len(clean)/4*4]
	out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
	k, err := base64.StdEncoding.Decode(out, clean)
	if err != nil {
		return nil, false
	}
	return out[:k], true
}
