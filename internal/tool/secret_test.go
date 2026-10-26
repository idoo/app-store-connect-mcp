package tool

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

const testPEM = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMN\n-----END PRIVATE KEY-----"

func TestLooksLikeSecret(t *testing.T) {
	der := "MIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQg" + strings.Repeat("Ab3+", 20)
	for s, want := range map[string]bool{
		testPEM: true,
		"key: " + base64.StdEncoding.EncodeToString([]byte(testPEM)): true,
		"body " + der: true,
		"eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.c2ln":       true,
		"A great app for tracking habits. MII is a word.": false,
		"MIGTAgEA short":              false,
		"https://example.com/privacy": false,
	} {
		if got := LooksLikeSecret(s); got != want {
			t.Errorf("LooksLikeSecret(%.40q) = %v, want %v", s, got, want)
		}
	}
}

func TestApprovalPromptWarnsOnSecrets(t *testing.T) {
	p := PlanPreview{
		Summary:  "update en-US metadata",
		Affected: []string{"PATCH /v1/appStoreVersionLocalizations/1"},
		Intended: map[string]any{"locales": map[string]any{"en-US": map[string]any{
			"description": testPEM,
			"keywords":    "habits,tracker",
		}}},
		Warnings: []string{"keywords replace the current set"},
	}
	got, err := ApprovalPrompt("metadata_apply", "work", p)
	if err != nil {
		t.Fatal(err)
	}
	want := "\nNotes: `keywords replace the current set`; `change.locales.en-US.description looks like a private key or token`\nTargets:"
	if !strings.Contains(got, want) {
		t.Fatalf("prompt = %q, want suffix %q", got, want)
	}
	p.Intended = map[string]any{"description": "plain"}
	if got, _ := ApprovalPrompt("metadata_apply", "work", p); strings.Contains(got, "looks like a private key") {
		t.Fatalf("clean preview warned: %q", got)
	}
}

// Certificates and provisioning profiles that signing tools return or upload
// are not secrets and never trigger the approval-prompt key warning.
func TestApprovalPromptIgnoresCertificatesAndProfiles(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cms := append([]byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x07, 0x02}, make([]byte, 400)...)
	profile := append([]byte{0x30, 0x82, byte(len(cms) >> 8), byte(len(cms))}, cms...)
	b64 := base64.StdEncoding.EncodeToString
	p := PlanPreview{
		Summary:  "register certificate",
		Affected: []string{"POST /v1/certificates"},
		Intended: map[string]any{
			"certificateContent": b64(cert),
			"pem":                string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})),
			"profileContent":     b64(profile),
		},
	}
	got, err := ApprovalPrompt("signing_profile_create", "work", p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "looks like a private key") {
		t.Fatalf("certificate/profile warned: %q", got)
	}
}
