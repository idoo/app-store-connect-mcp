package auth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// JWT parameters required by App Store Connect.
const (
	// Audience is the "aud" claim App Store Connect expects.
	Audience = "appstoreconnect-v1"
	// Lifetime is the token validity (Apple's maximum is 20 minutes).
	Lifetime = 19 * time.Minute
	// Backdate is subtracted from "iat" to tolerate clock skew.
	Backdate = 30 * time.Second
	// RefreshMargin is the remaining validity below which a cached token is replaced.
	RefreshMargin = 2 * time.Minute
)

// Token is a signed JWT and its expiry. String redacts the value.
type Token struct {
	// Value is the compact JWT.
	Value string
	// ExpiresAt is the "exp" claim.
	ExpiresAt time.Time
}

// String hides the JWT.
func (t Token) String() string {
	return "auth.Token{REDACTED, expires " + t.ExpiresAt.UTC().Format(time.RFC3339) + "}"
}

// GoString hides the JWT for %#v.
func (t Token) GoString() string { return t.String() }

// Signer creates ES256 JWTs for one App Store Connect key.
// An empty issuer id selects the Individual API key form (sub "user", no iss).
type Signer struct {
	key      *Key
	issuerID string
}

// NewSigner returns a Signer for key. issuerID is empty for individual keys.
func NewSigner(key *Key, issuerID string) (*Signer, error) {
	if key == nil || key.priv == nil {
		return nil, fmt.Errorf("new signer: %w", ErrEmptyKey)
	}
	return &Signer{key: key, issuerID: issuerID}, nil
}

// Individual reports whether the signer uses the Individual API key form.
func (s *Signer) Individual() bool { return s.issuerID == "" }

// String identifies the signer without key material.
func (s *Signer) String() string {
	return fmt.Sprintf("auth.Signer{kid:%s, individual:%t}", s.key.id, s.Individual())
}

// GoString redacts for %#v.
func (s *Signer) GoString() string { return s.String() }

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Iss string `json:"iss,omitempty"`
	Sub string `json:"sub,omitempty"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Aud string `json:"aud"`
}

// Sign returns a token issued at now-[Backdate] that expires [Lifetime] later.
func (s *Signer) Sign(now time.Time) (Token, error) {
	iat := now.Add(-Backdate).Truncate(time.Second)
	exp := iat.Add(Lifetime)
	claims := jwtClaims{Iat: iat.Unix(), Exp: exp.Unix(), Aud: Audience}
	if s.Individual() {
		claims.Sub = "user"
	} else {
		claims.Iss = s.issuerID
	}
	head, err := json.Marshal(jwtHeader{Alg: "ES256", Kid: s.key.id, Typ: "JWT"})
	if err != nil {
		return Token{}, fmt.Errorf("encode jwt header: %w", err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return Token{}, fmt.Errorf("encode jwt claims: %w", err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(body)
	sig, err := signES256(s.key.priv, []byte(signing))
	if err != nil {
		return Token{}, err
	}
	return Token{Value: signing + "." + enc.EncodeToString(sig), ExpiresAt: exp}, nil
}

// signES256 returns the JWS ES256 signature: r and s as 32-byte big-endian
// integers concatenated (RFC 7518 §3.4), not ASN.1 DER.
func signES256(priv *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	r, sv, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign jwt: %w", err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sv.FillBytes(out[32:])
	return out, nil
}
