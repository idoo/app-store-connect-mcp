package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ConfirmMode declares how a tool gates mutations. Write capability (see
// [IsWrite]) is granted only after the gate passes; a preview call runs
// read-only.
type ConfirmMode int

const (
	// ConfirmNone means no gate. Write tools receive write capability up front.
	ConfirmNone ConfirmMode = iota
	// ConfirmByFlag means In embeds [ConfirmFlag]; without confirm:true the call is
	// a preview. With it, the human is asked via elicitation when available.
	ConfirmByFlag
	// ConfirmByPlan means In embeds [ConfirmPlan]; the first call returns a preview
	// and a plan_id bound to the args and a state fingerprint; the second call
	// passes confirm:"<plan_id>" and is refused if the state changed.
	ConfirmByPlan
	// ConfirmByHuman is like ConfirmByFlag, but the human must approve via
	// elicitation; refused when elicitation is unavailable unless the profile
	// sets AllowNoElicit. For invites, webhooks, revocations and public replies.
	ConfirmByHuman
)

// ConfirmFlag is embedded in the input of [ConfirmByFlag] and
// [ConfirmByHuman] tools.
type ConfirmFlag struct {
	Confirm bool `json:"confirm,omitempty" jsonschema:"set true only after reviewing the preview of this exact call"`
}

// ConfirmPlan is embedded in the input of [ConfirmByPlan] tools.
type ConfirmPlan struct {
	Confirm string `json:"confirm,omitempty" jsonschema:"plan_id returned by the preview call with identical arguments"`
}

// PlanPreview describes what a mutation would do. The approval prompt shown
// to the human is built from Summary, Intended and Affected, so put the
// concrete payload there (reply text, invitee, webhook URL).
type PlanPreview struct {
	Summary      string   `json:"summary"`
	Current      any      `json:"current,omitempty"`
	Intended     any      `json:"intended,omitempty"`
	Affected     []string `json:"affected,omitempty"`
	Irreversible []string `json:"irreversible,omitempty"`
}

// previewBody is the payload of a preview result.
type previewBody struct {
	Status  string      `json:"status"`
	Preview PlanPreview `json:"preview"`
	PlanID  string      `json:"plan_id,omitempty"`
	Next    string      `json:"next"`
}

// RequireConfirm is the gate of [ConfirmByFlag] and [ConfirmByHuman] tools.
// It returns either a write-capable context (proceed) or a result the handler
// must return as-is:
//
//	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, preview)
//	if res != nil {
//		return res, nil
//	}
//	_, err := env.ASC.Do(wctx, req)
//
// Without confirm the result is a needs_confirm preview and nothing changes.
func RequireConfirm(ctx context.Context, env *Env, confirm bool, p PlanPreview) (context.Context, *Result) {
	m, res := gateMeta(ctx, env, ConfirmByFlag, ConfirmByHuman)
	if res != nil {
		return nil, res
	}
	if !confirm && !env.NoConfirm {
		return nil, OK(previewBody{
			Status:  ErrNeedsConfirm.Code,
			Preview: p,
			Next:    "nothing was changed; call again with the same arguments and confirm:true to apply",
		})
	}
	if res := approve(ctx, env, m, p); res != nil {
		return nil, res
	}
	return withWrite(ctx), nil
}

// Preview is the gate of [ConfirmByPlan] tools. args is the tool input (its
// "confirm" key is ignored); fingerprint is a [Fingerprint] of the remote state
// the plan depends on. Without a plan id it records a plan and returns the
// preview with its plan_id. With one it verifies the plan against the current
// fingerprint (stale → error result), asks the human when possible and returns
// a write-capable context.
func Preview(ctx context.Context, env *Env, planID string, args any, fingerprint string, p PlanPreview) (context.Context, *Result) {
	m, res := gateMeta(ctx, env, ConfirmByPlan)
	if res != nil {
		return nil, res
	}
	if fingerprint == "" {
		return nil, Fail(fmt.Errorf("%s: %w: empty state fingerprint", m.Name, ErrInternal))
	}
	if env.NoConfirm {
		return withWrite(ctx), nil
	}
	if planID == "" {
		id, err := env.Plans.Propose(ctx, m.Name, args, fingerprint, p)
		if err != nil {
			return nil, Fail(fmt.Errorf("propose plan: %w", err))
		}
		return nil, OK(previewBody{
			Status:  "planned",
			Preview: p,
			PlanID:  id,
			Next:    "nothing was changed; call again with the same arguments and confirm set to plan_id (valid 15 minutes)",
		})
	}
	if err := env.Plans.Verify(ctx, planID, m.Name, args, fingerprint); err != nil {
		return nil, Fail(err)
	}
	if res := approve(ctx, env, m, p); res != nil {
		return nil, res
	}
	return withWrite(ctx), nil
}

// Unconfirmed grants write capability without a gate, for branches a
// [ConfirmByFlag] or [ConfirmByPlan] tool has determined are pure creates or
// additive updates (nothing deleted, overwritten or made public). It is
// refused for [ConfirmByHuman] tools.
func Unconfirmed(ctx context.Context, env *Env) (context.Context, *Result) {
	if _, res := gateMeta(ctx, env, ConfirmByFlag, ConfirmByPlan); res != nil {
		return nil, res
	}
	return withWrite(ctx), nil
}

// gateMeta checks that ctx belongs to a write-enabled call of a tool declared
// with one of modes.
func gateMeta(ctx context.Context, env *Env, modes ...ConfirmMode) (*Meta, *Result) {
	m := metaFrom(ctx)
	if m == nil {
		return nil, Fail(fmt.Errorf("%w: confirm gate used outside Tool.Call", ErrInternal))
	}
	ok := false
	for _, mode := range modes {
		ok = ok || m.Confirm == mode
	}
	if !ok {
		return nil, Fail(fmt.Errorf("%w: %s uses the wrong confirm gate for its ConfirmMode", ErrInternal, m.Name))
	}
	if !env.Profile.AllowWrites {
		return nil, Fail(fmt.Errorf("%s: %w", m.Name, ErrWritesDisabled))
	}
	return m, nil
}

// approve asks the human with a framework-built prompt. Human-mode tools
// require an answer unless the profile allows proceeding without one.
func approve(ctx context.Context, env *Env, m *Meta, p PlanPreview) *Result {
	if env.NoConfirm && m.Confirm != ConfirmByHuman {
		return nil
	}
	ok, err := env.Elicit(ctx, ApprovalPrompt(m.Name, p))
	switch {
	case errors.Is(err, ErrElicitationUnavailable):
		if m.Confirm == ConfirmByHuman && !env.Profile.AllowNoElicit {
			return Fail(fmt.Errorf("%s: %w", m.Name, ErrHumanRequired))
		}
		return nil
	case err != nil:
		return Fail(fmt.Errorf("ask for confirmation: %w", err))
	case !ok:
		return Fail(ErrDeclined)
	}
	return nil
}

// maxPromptPart caps each JSON part of an approval prompt.
const maxPromptPart = 1500

// ApprovalPrompt renders the human approval question for a preview.
func ApprovalPrompt(toolName string, p PlanPreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hamn %s wants to: %s", toolName, p.Summary)
	if p.Intended != nil {
		b.WriteString("\nChange: " + compactJSON(p.Intended))
	}
	if len(p.Affected) > 0 {
		b.WriteString("\nAffects: " + strings.Join(p.Affected, ", "))
	}
	if len(p.Irreversible) > 0 {
		b.WriteString("\nIrreversible: " + strings.Join(p.Irreversible, "; "))
	}
	b.WriteString("\nApprove?")
	return b.String()
}

func compactJSON(v any) string {
	bs, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(bs) > maxPromptPart {
		return string(bs[:maxPromptPart]) + "…"
	}
	return string(bs)
}

// CanonicalArgs returns the canonical JSON of args with any top-level
// "confirm" key removed (map keys sorted by encoding/json). Planners bind
// plans to this encoding.
func CanonicalArgs(args any) ([]byte, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("canonical args: %w", err)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return b, nil // non-object args are canonical as marshaled
	}
	delete(m, "confirm")
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("canonical args: %w", err)
	}
	return out, nil
}

// Fingerprint returns a sha256 hex digest of the canonical JSON of state. Pass
// the remote fields the plan depends on (current attribute values,
// modification markers); any change makes a pending plan stale. It fails
// closed: an unmarshalable state is an error, never a constant digest.
func Fingerprint(state any) (string, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint: %v", ErrInternal, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
