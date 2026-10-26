package tool

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var approvalNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func testClaims() approvalClaims {
	return approvalClaims{Tool: "release_submit", Args: `{"app":"1"}`, Profile: "work", Plan: "plan-1", Prompt: "Submit 1.0?"}
}

func mustIssue(t *testing.T, s *approvalSigner, c approvalClaims, now time.Time) string {
	t.Helper()
	state, err := s.issue(c, now)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// decodeState splits a state into its claims and signature.
func decodeState(t *testing.T, state string) (approvalClaims, string) {
	t.Helper()
	payload, sig, ok := strings.Cut(state, ".")
	if !ok {
		t.Fatalf("state %q has no signature", state)
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var c approvalClaims
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c, sig
}

// encodeState re-encodes claims with sig, without re-signing.
func encodeState(t *testing.T, c approvalClaims, sig string) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + sig
}

func TestApprovalRedeemAccepts(t *testing.T) {
	s := newApprovalSigner()
	if err := s.redeem(mustIssue(t, s, testClaims(), approvalNow), testClaims(), approvalNow.Add(time.Minute)); err != nil {
		t.Fatalf("redeem = %v, want nil", err)
	}
}

// TestApprovalProfileMismatch pins `got.Profile != want.Profile` in redeem:
// a state approved under one profile never authorises a call under another.
func TestApprovalProfileMismatch(t *testing.T) {
	s := newApprovalSigner()
	state := mustIssue(t, s, testClaims(), approvalNow)
	want := testClaims()
	want.Profile = "personal"
	if err := s.redeem(state, want, approvalNow); !errors.Is(err, errApprovalMismatch) {
		t.Fatalf("redeem = %v, want approval mismatch", err)
	}
	// The refusal must not burn the nonce: the right profile still redeems.
	if err := s.redeem(state, testClaims(), approvalNow); err != nil {
		t.Fatalf("redeem with right profile = %v", err)
	}
}

// TestApprovalCallBinding covers the other bound fields: tool, args and
// prompt mismatch; a changed plan is a stale plan.
func TestApprovalCallBinding(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*approvalClaims)
		want error
	}{
		"tool":   {func(c *approvalClaims) { c.Tool = "release_cancel" }, errApprovalMismatch},
		"args":   {func(c *approvalClaims) { c.Args = `{"app":"2"}` }, errApprovalMismatch},
		"prompt": {func(c *approvalClaims) { c.Prompt = "Submit 2.0?" }, errApprovalMismatch},
		"plan":   {func(c *approvalClaims) { c.Plan = "plan-2" }, ErrStalePlan},
	} {
		t.Run(name, func(t *testing.T) {
			s := newApprovalSigner()
			state := mustIssue(t, s, testClaims(), approvalNow)
			want := testClaims()
			tc.mut(&want)
			if err := s.redeem(state, want, approvalNow); !errors.Is(err, tc.want) {
				t.Fatalf("redeem = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestApprovalBadSignature pins the `hmac.Equal` check in redeem: each
// claim field tampered under the original signature is refused, even when
// the verifier expects exactly the tampered call (so only the signature can
// catch it). Also covered: another process key, a truncated or missing
// signature, and a forged state not burning the genuine nonce.
func TestApprovalBadSignature(t *testing.T) {
	fields := map[string]func(*approvalClaims){
		"nonce":   func(c *approvalClaims) { c.Nonce = strings.Repeat("0", 32) },
		"tool":    func(c *approvalClaims) { c.Tool = "release_publish" },
		"args":    func(c *approvalClaims) { c.Args = `{"app":"666"}` },
		"profile": func(c *approvalClaims) { c.Profile = "victim" },
		"plan":    func(c *approvalClaims) { c.Plan = "plan-x" },
		"prompt":  func(c *approvalClaims) { c.Prompt = "Harmless?" },
		"issued":  func(c *approvalClaims) { c.Issued++ },
		"expires": func(c *approvalClaims) { c.Expires += 86400 },
	}
	for name, mut := range fields {
		t.Run(name, func(t *testing.T) {
			s := newApprovalSigner()
			state := mustIssue(t, s, testClaims(), approvalNow)
			c, sig := decodeState(t, state)
			mut(&c)
			want := approvalClaims{Tool: c.Tool, Args: c.Args, Profile: c.Profile, Plan: c.Plan, Prompt: c.Prompt}
			if err := s.redeem(encodeState(t, c, sig), want, approvalNow); !errors.Is(err, errApprovalMismatch) {
				t.Fatalf("tampered %s: redeem = %v, want approval mismatch", name, err)
			}
			if err := s.redeem(state, testClaims(), approvalNow); err != nil {
				t.Fatalf("genuine state after forgery: redeem = %v (forgery burned the nonce?)", err)
			}
		})
	}

	s := newApprovalSigner()
	other := newApprovalSigner()
	state := mustIssue(t, s, testClaims(), approvalNow)
	payload, sig, _ := strings.Cut(state, ".")
	for name, forged := range map[string]string{
		"other key":   mustIssue(t, other, testClaims(), approvalNow),
		"no sig":      payload,
		"empty sig":   payload + ".",
		"short sig":   payload + "." + sig[:len(sig)-1],
		"flipped sig": payload + "." + flip(sig),
		"empty":       "",
	} {
		if err := s.redeem(forged, testClaims(), approvalNow); !errors.Is(err, errApprovalMismatch) {
			t.Errorf("%s: redeem = %v, want approval mismatch", name, err)
		}
	}
}

func flip(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// TestApprovalExpired pins `!now.Before(time.Unix(got.Expires, 0))` in
// redeem: a state is good until just before ApprovalTTL and refused from it.
func TestApprovalExpired(t *testing.T) {
	s := newApprovalSigner()
	if err := s.redeem(mustIssue(t, s, testClaims(), approvalNow), testClaims(), approvalNow.Add(ApprovalTTL-time.Second)); err != nil {
		t.Fatalf("just before expiry: redeem = %v", err)
	}
	for _, at := range []time.Duration{ApprovalTTL, ApprovalTTL + time.Second, 24 * time.Hour} {
		state := mustIssue(t, s, testClaims(), approvalNow)
		if err := s.redeem(state, testClaims(), approvalNow.Add(at)); !errors.Is(err, errApprovalMismatch) {
			t.Errorf("at +%v: redeem = %v, want approval mismatch", at, err)
		}
	}
}

// TestApprovalReplay pins the `s.used[got.Nonce]` duplicate check in
// redeem: a state is accepted exactly once, also by another call site
// (the signer is process-wide).
func TestApprovalReplay(t *testing.T) {
	s := newApprovalSigner()
	state := mustIssue(t, s, testClaims(), approvalNow)
	if err := s.redeem(state, testClaims(), approvalNow); err != nil {
		t.Fatalf("first redeem = %v", err)
	}
	for i, at := range []time.Duration{0, time.Second, ApprovalTTL - time.Second} {
		if err := s.redeem(state, testClaims(), approvalNow.Add(at)); !errors.Is(err, errApprovalMismatch) {
			t.Fatalf("replay %d at +%v: redeem = %v, want approval mismatch", i, at, err)
		}
	}
}

// TestApprovalUsedCleanup pins the expiry sweep over s.used in redeem:
// redeemed nonces are remembered until their state expires (so replay stays
// refused) and dropped afterwards, so the map does not grow without bound.
func TestApprovalUsedCleanup(t *testing.T) {
	s := newApprovalSigner()
	first := mustIssue(t, s, testClaims(), approvalNow)
	if err := s.redeem(first, testClaims(), approvalNow); err != nil {
		t.Fatal(err)
	}
	firstClaims, _ := decodeState(t, first)

	// A later redeem before expiry keeps the first nonce.
	mid := approvalNow.Add(ApprovalTTL / 2)
	if err := s.redeem(mustIssue(t, s, testClaims(), mid), testClaims(), mid); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.used[firstClaims.Nonce]; !ok || len(s.used) != 2 {
		t.Fatalf("used = %v, want both nonces before expiry", s.used)
	}

	// A redeem after the first state expired sweeps it.
	late := approvalNow.Add(ApprovalTTL + time.Second)
	if err := s.redeem(mustIssue(t, s, testClaims(), late), testClaims(), late); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.used[firstClaims.Nonce]; ok {
		t.Fatalf("expired nonce still in used: %v", s.used)
	}
	if len(s.used) != 2 {
		t.Fatalf("used has %d entries, want 2 (mid + late)", len(s.used))
	}
}
