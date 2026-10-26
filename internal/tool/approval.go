package tool

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ApprovalTTL is how long an approval request stays answerable.
const ApprovalTTL = 10 * time.Minute

// ApprovalAsk is a request for human approval: the server renders it as an
// MCP input-required result (one form elicitation; Accept means yes) whose
// requestState is State. Nothing was changed when it is returned.
type ApprovalAsk struct {
	// Prompt is the [ApprovalPrompt] shown to the human.
	Prompt string
	// State is the signed, one-shot token the client echoes on its retry.
	State string
}

// ApprovalReply is the client's answer to an [ApprovalAsk], taken from the
// retried tools/call (MRTR inputResponses and requestState).
type ApprovalReply struct {
	// State is the echoed requestState.
	State string
	// Action is the approval form's answer (accept, decline or cancel); ""
	// when the response is missing or not an elicitation result.
	Action string
}

// approvalClaims is the signed payload of an approval requestState. It binds
// the approval to one call: tool, canonical args, profile, plan and the exact
// prompt the human saw.
type approvalClaims struct {
	Nonce   string `json:"n"`
	Tool    string `json:"t"`
	Args    string `json:"a"`
	Profile string `json:"p"`
	Plan    string `json:"f,omitempty"`
	Prompt  string `json:"q"`
	Issued  int64  `json:"i"`
	Expires int64  `json:"e"`
}

// approvalSigner signs approval states with a per-process key and remembers
// redeemed nonces until they expire, so each state is accepted once.
type approvalSigner struct {
	key  []byte
	mu   sync.Mutex
	used map[string]time.Time
}

// approvals is the process-wide signer: a state issued by one session or
// server instance verifies in any other of the same process.
var approvals = newApprovalSigner()

func newApprovalSigner() *approvalSigner {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("tool: approval key: %v", err))
	}
	return &approvalSigner{key: key, used: map[string]time.Time{}}
}

// errApprovalMismatch refuses a reply whose state is missing, forged,
// expired, replayed or bound to another call.
var errApprovalMismatch = fmt.Errorf("%w: approval expired or does not match; call again", ErrInvalidArgs)

func (s *approvalSigner) sign(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// issue returns a signed state for c, valid for [ApprovalTTL] from now.
func (s *approvalSigner) issue(c approvalClaims, now time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("%w: approval nonce: %v", ErrInternal, err)
	}
	c.Nonce = hex.EncodeToString(nonce)
	c.Issued = now.Unix()
	c.Expires = now.Add(ApprovalTTL).Unix()
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: approval state: %v", ErrInternal, err)
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + s.sign(payload), nil
}

// redeem verifies state against want and consumes its nonce. The signature
// is checked first in constant time, so a forged state never burns a nonce.
// A state for the same call whose plan or remote state changed fails with
// [ErrStalePlan]; anything else fails with errApprovalMismatch.
func (s *approvalSigner) redeem(state string, want approvalClaims, now time.Time) error {
	payload, sig, ok := strings.Cut(state, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return errApprovalMismatch
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return errApprovalMismatch
	}
	var got approvalClaims
	if err := json.Unmarshal(b, &got); err != nil || got.Nonce == "" {
		return errApprovalMismatch
	}
	if !now.Before(time.Unix(got.Expires, 0)) {
		return errApprovalMismatch
	}
	if got.Tool != want.Tool || got.Args != want.Args || got.Profile != want.Profile {
		return errApprovalMismatch
	}
	if got.Plan != want.Plan {
		return fmt.Errorf("%w: the remote state changed after approval was asked; preview again", ErrStalePlan)
	}
	if got.Prompt != want.Prompt {
		return errApprovalMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, exp := range s.used {
		if !now.Before(exp) {
			delete(s.used, n)
		}
	}
	if _, dup := s.used[got.Nonce]; dup {
		return errApprovalMismatch
	}
	s.used[got.Nonce] = time.Unix(got.Expires, 0)
	return nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// AskHuman runs the human-approval round for prompt inside [Tool.Call]. It
// returns (nil, nil) when the human accepted on this call; a non-nil result
// (an [ApprovalAsk]) the handler must return as-is; or an error wrapping
// [ErrElicitationUnavailable] (the client cannot show a form), [ErrDeclined],
// [ErrStalePlan] or [ErrInvalidArgs] (missing, forged, expired, replayed or
// mismatched approval). Nothing may be written unless it returns (nil, nil).
func AskHuman(ctx context.Context, env *Env, prompt string) (*Result, error) {
	c := callFrom(ctx)
	if c == nil {
		return nil, fmt.Errorf("%w: AskHuman used outside Tool.Call", ErrInternal)
	}
	return askHuman(env, c, prompt, "")
}

// askHuman issues an approval request on the first round and verifies the
// client's reply on the retry. plan binds plan tools to their plan id and
// state fingerprint.
func askHuman(env *Env, c *call, prompt, plan string) (*Result, error) {
	if !env.CanElicit {
		return nil, ErrElicitationUnavailable
	}
	args, err := json.Marshal(c.args)
	if err != nil {
		return nil, fmt.Errorf("%w: approval args: %v", ErrInternal, err)
	}
	want := approvalClaims{
		Tool:    c.meta.Name,
		Args:    sha256Hex(args),
		Profile: env.Profile.Name,
		Plan:    plan,
		Prompt:  sha256Hex([]byte(prompt)),
	}
	now := env.Clock.Now()
	if env.Reply == nil {
		state, err := approvals.issue(want, now)
		if err != nil {
			return nil, err
		}
		return &Result{Ask: &ApprovalAsk{Prompt: prompt, State: state}}, nil
	}
	if env.Reply.Action == "" {
		return nil, errApprovalMismatch
	}
	if err := approvals.redeem(env.Reply.State, want, now); err != nil {
		return nil, err
	}
	if env.Reply.Action != "accept" {
		return nil, ErrDeclined
	}
	return nil, nil
}
