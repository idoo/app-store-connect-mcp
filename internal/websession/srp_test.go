package websession

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

// detReader yields a fixed byte stream (deterministic SRP secrets in tests).
type detReader byte

func (d *detReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(*d)
		*d++
	}
	return len(p), nil
}

// srpTestServer is the server half of Apple's SRP-6a, for tests only.
type srpTestServer struct {
	salt, derived []byte
	v, b, B       *big.Int
}

func newSRPTestServer(t *testing.T, password string, salt []byte, iter int, protocol string, seed byte) *srpTestServer {
	t.Helper()
	derived, err := derivePassword(protocol, password, salt, iter)
	if err != nil {
		t.Fatal(err)
	}
	x := srpX(salt, derived)
	v := new(big.Int).Exp(srpG, x, srpN)
	r := detReader(seed)
	bb := make([]byte, 32)
	_, _ = r.Read(bb)
	b := new(big.Int).SetBytes(bb)
	B := new(big.Int).Mul(srpK(), v)
	B.Add(B, new(big.Int).Exp(srpG, b, srpN)).Mod(B, srpN)
	return &srpTestServer{salt: salt, derived: derived, v: v, b: b, B: B}
}

// expected returns the M1 the server requires and the M2 it would return.
func (s *srpTestServer) expected(account string, aBytes []byte) (m1, m2 []byte) {
	A := new(big.Int).SetBytes(aBytes)
	u := srpU(A, s.B)
	S := new(big.Int).Exp(s.v, u, srpN)
	S.Mul(S, A).Mod(S, srpN)
	S.Exp(S, s.b, srpN)
	key := srpSessionKey(S)
	m1 = srpM1(account, s.salt, A, s.B.Bytes(), key)
	return m1, srpM2(A, m1, key)
}

func TestSRPGroupIsSafePrime(t *testing.T) {
	if srpN == nil || srpN.BitLen() != 2048 || !srpN.ProbablyPrime(4) {
		t.Fatal("srpN is not the 2048-bit RFC 5054 prime")
	}
	q := new(big.Int).Rsh(srpN, 1)
	if !q.ProbablyPrime(4) {
		t.Fatal("(N-1)/2 is not prime; group constant mistyped")
	}
}

func TestSRPClientServerAgree(t *testing.T) {
	for _, protocol := range []string{"s2k", "s2k_fo"} {
		salt := []byte("0123456789abcdef")
		srv := newSRPTestServer(t, "correct horse", salt, 1000, protocol, 7)
		r := detReader(42)
		c, err := newSRPClient(&r)
		if err != nil {
			t.Fatal(err)
		}
		m1, m2, err := c.proof("user@example.com", salt, srv.B.Bytes(), srv.derived)
		if err != nil {
			t.Fatal(err)
		}
		wantM1, wantM2 := srv.expected("user@example.com", c.publicA())
		if !bytes.Equal(m1, wantM1) || !bytes.Equal(m2, wantM2) {
			t.Fatalf("%s: client and server proofs disagree", protocol)
		}

		wrong, _ := derivePassword(protocol, "wrong", salt, 1000)
		bad, _, err := c.proof("user@example.com", salt, srv.B.Bytes(), wrong)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(bad, wantM1) {
			t.Fatalf("%s: wrong password produced the right proof", protocol)
		}
	}
}

func TestSRPRejectsBadServerKey(t *testing.T) {
	r := detReader(1)
	c, err := newSRPClient(&r)
	if err != nil {
		t.Fatal(err)
	}
	for _, B := range [][]byte{nil, {0}, srpN.Bytes(), make([]byte, srpNLen+1)} {
		if _, _, err := c.proof("u", []byte("s"), B, make([]byte, 32)); err == nil {
			t.Fatalf("accepted invalid B of %d bytes", len(B))
		}
	}
}

// Reference: rudrank's hex-string implementation (internal/web/auth.go),
// re-typed here to pin byte-for-byte compatibility with the client that is
// known to work against Apple.
func refHashPad(values ...string) *big.Int {
	nLen := 2 * srpNLen
	var in strings.Builder
	for _, v := range values {
		in.WriteString(strings.Repeat("0", nLen-len(v)))
		in.WriteString(v)
	}
	raw, _ := hex.DecodeString(in.String())
	sum := sha256.Sum256(raw)
	return new(big.Int).Mod(new(big.Int).SetBytes(sum[:]), srpN)
}

func refNumHex(n *big.Int) string {
	h := n.Text(16)
	if len(h)%2 == 1 {
		h = "0" + h
	}
	return h
}

func refShaHex(h string) string {
	raw, _ := hex.DecodeString(h)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func refProof(user string, a, A *big.Int, serverB, derived, salt []byte) (string, string) {
	bHex, saltHex, aHex := hex.EncodeToString(serverB), hex.EncodeToString(salt), refNumHex(A)
	x, _ := new(big.Int).SetString(refShaHex(saltHex+refShaHex("3a"+hex.EncodeToString(derived))), 16)
	k := refHashPad(refNumHex(srpN), refNumHex(srpG))
	u := refHashPad(aHex, bHex)
	B := new(big.Int).SetBytes(serverB)
	kgx := new(big.Int).Mul(k, new(big.Int).Exp(srpG, x, srpN))
	base := new(big.Int).Sub(B, kgx.Mod(kgx, srpN))
	base.Mod(base, srpN)
	S := new(big.Int).Exp(base, new(big.Int).Add(a, new(big.Int).Mul(u, x)), srpN)
	kHex := refShaHex(refNumHex(S))
	hxor := new(big.Int).Xor(refHashPad(refNumHex(srpN)), refHashPad(refNumHex(srpG)))
	us := sha256.Sum256([]byte(user))
	m1 := refShaHex(refNumHex(hxor) + hex.EncodeToString(us[:]) + saltHex + aHex + bHex + kHex)
	m2raw, _ := hex.DecodeString(aHex + m1 + kHex)
	m2sum := sha256.Sum256(m2raw)
	return m1, refNumHex(new(big.Int).SetBytes(m2sum[:]))
}

func TestSRPMatchesReferenceClient(t *testing.T) {
	salt := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	srv := newSRPTestServer(t, "pässwörd", salt, 20000, "s2k", 99)
	r := detReader(3)
	c, err := newSRPClient(&r)
	if err != nil {
		t.Fatal(err)
	}
	m1, m2, err := c.proof("Someone@Example.com", salt, srv.B.Bytes(), srv.derived)
	if err != nil {
		t.Fatal(err)
	}
	refM1, refM2 := refProof("Someone@Example.com", c.a, c.A, srv.B.Bytes(), srv.derived, salt)
	if hex.EncodeToString(m1) != refM1 || hex.EncodeToString(m2) != refM2 {
		t.Fatalf("proof differs from reference client:\n m1 %x\nref %s\n m2 %x\nref %s", m1, refM1, m2, refM2)
	}
}

// refPBKDF2 is an independent PBKDF2-HMAC-SHA256 (one 32-byte block).
func refPBKDF2(password, salt []byte, iter int) []byte {
	mac := hmac.New(sha256.New, password)
	mac.Write(salt)
	_ = binary.Write(mac, binary.BigEndian, uint32(1))
	u := mac.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}

func TestDerivePassword(t *testing.T) {
	salt := []byte("NaCl-salt")
	digest := sha256.Sum256([]byte("hunter2"))
	cases := map[string][]byte{
		"s2k":    digest[:],
		"s2k_fo": []byte(hex.EncodeToString(digest[:])),
	}
	for protocol, prepared := range cases {
		got, err := derivePassword(protocol, "hunter2", salt, 1234)
		if err != nil {
			t.Fatal(err)
		}
		if want := refPBKDF2(prepared, salt, 1234); !bytes.Equal(got, want) {
			t.Fatalf("%s: derived %x, want %x", protocol, got, want)
		}
	}
	s2k, _ := derivePassword("s2k", "hunter2", salt, 1234)
	fo, _ := derivePassword("s2k_fo", "hunter2", salt, 1234)
	if bytes.Equal(s2k, fo) {
		t.Fatal("s2k and s2k_fo must differ")
	}
	for _, bad := range []struct {
		protocol string
		iter     int
		salt     []byte
	}{{"s2k", 0, salt}, {"s2k", srpMaxIterations + 1, salt}, {"s2k", 10, nil}, {"plain", 10, salt}} {
		_, err := derivePassword(bad.protocol, "hunter2", bad.salt, bad.iter)
		if err == nil {
			t.Fatalf("accepted %+v", bad)
		}
		if strings.Contains(fmt.Sprint(err), "hunter2") {
			t.Fatal("error leaks password")
		}
	}
}
