package secrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testMaterial returns a fresh P-256 key as PKCS#8 and SEC1 DER, and a
// self-signed certificate DER for it.
func testMaterial(t *testing.T) (pkcs8, sec1, cert []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if pkcs8, err = x509.MarshalPKCS8PrivateKey(key); err != nil {
		t.Fatal(err)
	}
	if sec1, err = x509.MarshalECPrivateKey(key); err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Apple Distribution: Test"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour),
	}
	if cert, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key); err != nil {
		t.Fatal(err)
	}
	return pkcs8, sec1, cert
}

// fakeProfile is a CMS SignedData-shaped DER (what profileContent decodes
// to): SEQUENCE, OID 1.2.840.113549.1.7.2, then filler.
func fakeProfile() []byte {
	body := append([]byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x07, 0x02}, make([]byte, 400)...)
	return append([]byte{0x30, 0x82, byte(len(body) >> 8), byte(len(body))}, body...)
}

func TestContainsPrivateKeyOrToken(t *testing.T) {
	pkcs8, sec1, cert := testMaterial(t)
	b64 := base64.StdEncoding.EncodeToString
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}))
	for name, tc := range map[string]struct {
		s    string
		want bool
	}{
		"pem key":              {keyPEM, true},
		"ec pem key":           {string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})), true},
		"base64 pem key":       {"key: " + b64([]byte(keyPEM)), true},
		"der pkcs8 body":       {"body " + b64(pkcs8), true},
		"der sec1 body":        {"body " + b64(sec1), true},
		"jwt":                  {"eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.c2ln", true},
		"certificate pem":      {certPEM, false},
		"base64 certificate":   {`{"certificateContent":"` + b64(cert) + `"}`, false},
		"base64 pem cert":      {b64([]byte(certPEM)), false},
		"provisioning profile": {`{"profileContent":"` + b64(fakeProfile()) + `"}`, false},
		"prose":                {"A great app for tracking habits. MII is a word.", false},
		"short der":            {"MIGTAgEA short", false},
		"url":                  {"https://example.com/privacy", false},
	} {
		if got := ContainsPrivateKeyOrToken(tc.s); got != tc.want {
			t.Errorf("%s: ContainsPrivateKeyOrToken = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsPrivateKeyDER(t *testing.T) {
	pkcs8, sec1, cert := testMaterial(t)
	for name, tc := range map[string]struct {
		der  []byte
		want bool
	}{
		"pkcs8": {pkcs8, true}, "sec1": {sec1, true}, "cert": {cert, false},
		"profile": {fakeProfile(), false}, "empty": {nil, false}, "truncated": {[]byte{0x30, 0x82}, false},
	} {
		if got := IsPrivateKeyDER(tc.der); got != tc.want {
			t.Errorf("%s: IsPrivateKeyDER = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDERKeyBase64EscapedNewlines(t *testing.T) {
	pkcs8, _, _ := testMaterial(t)
	s := base64.StdEncoding.EncodeToString(pkcs8)
	s = s[:20] + `\n` + s[20:]
	if !ContainsPrivateKeyOrToken(`"k":"` + s + `"`) {
		t.Fatal("JSON-escaped DER key body not detected")
	}
	if strings.Contains(s, "BEGIN") {
		t.Fatal("bad fixture")
	}
}
