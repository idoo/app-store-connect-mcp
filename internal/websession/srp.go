// Apple ID SRP-6a client. Protocol details follow
// rudrankriyam/App-Store-Connect-CLI (MIT), internal/web/auth.go, which in
// turn matches Apple's GrandSlam web sign-in: RFC 5054 2048-bit group,
// SHA-256, x = H(salt | H(":" | pbkdf2(password))) with no username, and
// Apple's byte layout for M1/M2.

package websession

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// srpNHex is the RFC 5054 appendix A 2048-bit group prime (generator 2).
const srpNHex = "AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050" +
	"A37329CBB4A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50" +
	"E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B8" +
	"55F97993EC975EEAA80D740ADBF4FF747359D041D5C33EA71D281E446B14773B" +
	"CA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748" +
	"544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB3786160279004E57AE6" +
	"AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8E9DBFBB6" +
	"94B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F9E4AFF73"

const (
	// srpSecretBytes is the size of the client's ephemeral secret a.
	srpSecretBytes = 256
	// srpDerivedLen is the PBKDF2 output length Apple expects.
	srpDerivedLen = 32
	// srpMaxIterations bounds the server-chosen PBKDF2 cost.
	srpMaxIterations = 10_000_000
)

// srpN is the group prime; srp_test.go checks it is a safe prime.
var (
	srpN, _ = new(big.Int).SetString(srpNHex, 16)
	srpG    = big.NewInt(2)
	srpNLen = (srpN.BitLen() + 7) / 8
)

// errSRPBadServerKey means the server's public value B was unusable.
var errSRPBadServerKey = errors.New("websession: Apple sent an invalid SRP server key")

// srpClient holds one sign-in attempt's ephemeral key pair. Its values are
// secrets: never log or print them.
type srpClient struct {
	a *big.Int
	A *big.Int
}

// newSRPClient draws a fresh ephemeral secret from r.
func newSRPClient(r io.Reader) (*srpClient, error) {
	buf := make([]byte, srpSecretBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("websession: generate SRP secret: %w", err)
	}
	a := new(big.Int).SetBytes(buf)
	if a.Sign() == 0 {
		return nil, errors.New("websession: generate SRP secret: zero secret")
	}
	return &srpClient{a: a, A: new(big.Int).Exp(srpG, a, srpN)}, nil
}

// publicA is the client public value A as Apple expects it (minimal bytes).
func (c *srpClient) publicA() []byte { return c.A.Bytes() }

// proof computes the client proof M1 and the expected server proof M2.
func (c *srpClient) proof(accountName string, salt, serverB, derived []byte) (m1, m2 []byte, err error) {
	if len(serverB) == 0 || len(serverB) > srpNLen {
		return nil, nil, errSRPBadServerKey
	}
	B := new(big.Int).SetBytes(serverB)
	if new(big.Int).Mod(B, srpN).Sign() == 0 {
		return nil, nil, errSRPBadServerKey
	}
	u := srpU(c.A, B)
	if u.Sign() == 0 {
		return nil, nil, errSRPBadServerKey
	}
	x := srpX(salt, derived)
	k := srpK()

	// S = (B - k*g^x) ^ (a + u*x) mod N
	kgx := new(big.Int).Exp(srpG, x, srpN)
	kgx.Mul(kgx, k).Mod(kgx, srpN)
	base := new(big.Int).Sub(B, kgx)
	base.Mod(base, srpN)
	exp := new(big.Int).Mul(u, x)
	exp.Add(exp, c.a)
	S := new(big.Int).Exp(base, exp, srpN)

	key := srpSessionKey(S)
	m1 = srpM1(accountName, salt, c.A, serverB, key)
	m2 = srpM2(c.A, m1, key)
	return m1, m2, nil
}

// srpHash is SHA-256 over the concatenated parts.
func srpHash(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// srpPad left-pads b with zeros to the group's byte length.
func srpPad(b []byte) []byte {
	if len(b) >= srpNLen {
		return b
	}
	out := make([]byte, srpNLen)
	copy(out[srpNLen-len(b):], b)
	return out
}

// srpHashInt is H(parts) as an integer reduced mod N.
func srpHashInt(parts ...[]byte) *big.Int {
	v := new(big.Int).SetBytes(srpHash(parts...))
	return v.Mod(v, srpN)
}

// srpK is the multiplier k = H(N | pad(g)).
func srpK() *big.Int { return srpHashInt(srpPad(srpN.Bytes()), srpPad(srpG.Bytes())) }

// srpU is the scrambling parameter u = H(pad(A) | pad(B)).
func srpU(A, B *big.Int) *big.Int { return srpHashInt(srpPad(A.Bytes()), srpPad(B.Bytes())) }

// srpX is Apple's private key x = H(salt | H(":" | derived)); the username is
// deliberately not part of it.
func srpX(salt, derived []byte) *big.Int {
	inner := srpHash([]byte(":"), derived)
	return new(big.Int).SetBytes(srpHash(salt, inner))
}

// srpSessionKey is K = H(S) over S's minimal big-endian bytes.
func srpSessionKey(S *big.Int) []byte {
	b := S.Bytes()
	if len(b) == 0 {
		b = []byte{0}
	}
	return srpHash(b)
}

// srpM1 is H(H(N) xor H(pad g) | H(I) | salt | A | B | K).
func srpM1(accountName string, salt []byte, A *big.Int, serverB, key []byte) []byte {
	hn := srpHash(srpPad(srpN.Bytes()))
	hg := srpHash(srpPad(srpG.Bytes()))
	x := make([]byte, len(hn))
	for i := range hn {
		x[i] = hn[i] ^ hg[i]
	}
	hxor := new(big.Int).SetBytes(x).Bytes()
	return srpHash(hxor, srpHash([]byte(accountName)), salt, A.Bytes(), serverB, key)
}

// srpM2 is H(A | M1 | K) with leading zero bytes dropped, as Apple's
// reference client sends it.
func srpM2(A *big.Int, m1, key []byte) []byte {
	return new(big.Int).SetBytes(srpHash(A.Bytes(), m1, key)).Bytes()
}

// derivePassword turns the password into SRP's input using Apple's protocol
// from signin/init: "s2k" feeds PBKDF2 the raw SHA-256 digest, "s2k_fo" its
// lowercase hex.
func derivePassword(protocol, password string, salt []byte, iterations int) ([]byte, error) {
	if iterations <= 0 || iterations > srpMaxIterations {
		return nil, fmt.Errorf("websession: Apple sent an unusable password iteration count (%d)", iterations)
	}
	if len(salt) == 0 {
		return nil, errors.New("websession: Apple sent an empty password salt")
	}
	digest := sha256.Sum256([]byte(password))
	var prepared string
	switch strings.TrimSpace(protocol) {
	case "s2k":
		prepared = string(digest[:])
	case "s2k_fo":
		prepared = hex.EncodeToString(digest[:])
	default:
		return nil, fmt.Errorf("websession: Apple asked for an unsupported password protocol %s", sanitizeProtocol(protocol))
	}
	key, err := pbkdf2.Key(sha256.New, prepared, salt, iterations, srpDerivedLen)
	if err != nil {
		return nil, fmt.Errorf("websession: derive password key: %w", err)
	}
	return key, nil
}

// sanitizeProtocol bounds a server-provided protocol name for error text.
func sanitizeProtocol(p string) string {
	var b strings.Builder
	for _, r := range p {
		if b.Len() >= 16 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "(unnamed)"
	}
	return b.String()
}
