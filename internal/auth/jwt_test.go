package auth

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var fixedNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// verifyJWT checks the ES256 raw r||s signature and returns decoded header and claims.
func verifyJWT(t *testing.T, tok string, pub *ecdsa.PublicKey) (header, claims map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts, want 3", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (raw r||s)", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("signature does not verify")
	}
	decode := func(seg string) map[string]any {
		raw, err := base64.RawURLEncoding.DecodeString(seg)
		if err != nil {
			t.Fatalf("decode segment: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal segment: %v", err)
		}
		return m
	}
	return decode(parts[0]), decode(parts[1])
}

func TestSignerSign(t *testing.T) {
	priv := newP256(t)
	key, err := NewKey("KEY123", pemPKCS8(t, priv))
	if err != nil {
		t.Fatal(err)
	}
	iat := float64(fixedNow.Add(-30 * time.Second).Unix())
	exp := iat + float64(19*60)
	tests := []struct {
		name       string
		issuer     string
		wantClaims map[string]any
	}{
		{
			name:       "team key",
			issuer:     "69a6de70-03db-47e3-e053-5b8c7c11a4d1",
			wantClaims: map[string]any{"iss": "69a6de70-03db-47e3-e053-5b8c7c11a4d1", "iat": iat, "exp": exp, "aud": "appstoreconnect-v1"},
		},
		{
			name:       "individual key",
			issuer:     "",
			wantClaims: map[string]any{"sub": "user", "iat": iat, "exp": exp, "aud": "appstoreconnect-v1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSigner(key, tc.issuer)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := s.Sign(fixedNow)
			if err != nil {
				t.Fatal(err)
			}
			if want := time.Unix(int64(exp), 0); !tok.ExpiresAt.Equal(want) {
				t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, want)
			}
			header, claims := verifyJWT(t, tok.Value, &priv.PublicKey)
			wantHeader := map[string]any{"alg": "ES256", "kid": "KEY123", "typ": "JWT"}
			if d := cmp.Diff(wantHeader, header); d != "" {
				t.Errorf("header (-want +got):\n%s", d)
			}
			if d := cmp.Diff(tc.wantClaims, claims); d != "" {
				t.Errorf("claims (-want +got):\n%s", d)
			}
		})
	}
}

func TestNewSignerRejectsNilKey(t *testing.T) {
	if _, err := NewSigner(nil, "iss"); err == nil {
		t.Fatal("expected error for nil key")
	}
}
