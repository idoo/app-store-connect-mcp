package asc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestRedactEncodedKeys reproduces L5: ASC_PRIVATE_KEY's base64 form and a
// .p8 body without its PEM lines passed redaction untouched.
func TestRedactEncodedKeys(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	sec1, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsa8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	wrapped := strings.Join(chunk(b64(pkcs8), 64), `\n`)
	for name, secret := range map[string]string{
		"base64 PEM":           b64([]byte(pemText)),
		"bare PKCS#8 P-256":    b64(pkcs8),
		"bare PKCS#8 RSA":      b64(rsa8),
		"bare SEC1":            b64(sec1),
		"escaped-wrapped body": wrapped,
	} {
		in := `{"note":"` + secret + `","other":"keep"}`
		out := Redact(in)
		if strings.Contains(out, secret[:40]) || !strings.Contains(out, redactedPEM) || !strings.Contains(out, `"other":"keep"`) {
			t.Errorf("%s: not redacted: %.120s", name, out)
		}
	}
	if got := Redact("key=" + b64(pkcs8)); got != "key="+redactedPEM {
		t.Errorf("boundary kept = %q", got)
	}
}

// Ordinary base64 (images, certificates, profiles) must survive.
func TestRedactKeepsOrdinaryBase64(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ec.PublicKey, ec)
	if err != nil {
		t.Fatal(err)
	}
	png := make([]byte, 300)
	copy(png, "\x89PNG\r\n\x1a\n")
	for name, in := range map[string]string{
		"png data URI":     `{"img":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"}`,
		"certificate":      `{"certificateContent":"` + base64.StdEncoding.EncodeToString(cert) + `"}`,
		"MII-looking text": `{"x":"MII` + strings.Repeat("A", 120) + `"}`,
	} {
		if out := Redact(in); out != in {
			t.Errorf("%s redacted: %.120s", name, out)
		}
	}
}

func chunk(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
