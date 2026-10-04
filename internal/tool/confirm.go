package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// a preview. With it, the human is asked via elicitation when available
	// (mandatory with [Meta.RequireHuman]); an agent-only confirm must follow a
	// preview of the same arguments in the session ([Planner.Claim]).
	ConfirmByFlag
	// ConfirmByPlan means In embeds [ConfirmPlan]; the first call returns a preview
	// and a plan_id bound to the args and a state fingerprint; the second call
	// passes plan_id:"<plan_id>" and is refused if the state changed.
	ConfirmByPlan
)

// ConfirmFlag is embedded in the input of [ConfirmByFlag] tools.
type ConfirmFlag struct {
	Confirm bool `json:"confirm,omitempty" jsonschema:"true after reviewing the preview"`
}

// ConfirmPlan is embedded in the input of [ConfirmByPlan] tools.
type ConfirmPlan struct {
	PlanID string `json:"plan_id,omitempty" jsonschema:"from the preview call"`
}

// PlanPreview describes what a mutation would do. The approval prompt shown
// to the human is built from Summary, Affected (targets: method and path,
// URLs, emails), Intended (the full payload, e.g. reply texts) and
// Irreversible, never truncated. Untrusted holds third-party text (e.g. the
// customer review being answered); it is shown after the payload, fenced and
// labeled as data.
type PlanPreview struct {
	Summary      string   `json:"summary"`
	Current      any      `json:"current,omitempty"`
	Intended     any      `json:"intended,omitempty"`
	Affected     []string `json:"affected,omitempty"`
	Irreversible []string `json:"irreversible,omitempty"`
	Untrusted    []string `json:"untrusted,omitempty"`
}

// previewBody is the payload of a preview result.
type previewBody struct {
	Status  string      `json:"status"`
	Preview PlanPreview `json:"preview"`
	PlanID  string      `json:"plan_id,omitempty"`
	Next    string      `json:"next"`
}

// RequireConfirm is the gate of [ConfirmByFlag] tools.
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
// A confirm the human did not approve is refused with [ErrPreviewRequired]
// unless a preview of the same arguments came first (single use).
func RequireConfirm(ctx context.Context, env *Env, confirm bool, p PlanPreview) (context.Context, *Result) {
	return requireConfirm(ctx, env, confirm, p, false)
}

// RequireHumanConfirm is [RequireConfirm] with human approval forced for this
// call, as if the tool declared [Meta.RequireHuman]: for branches of a
// [ConfirmByFlag] tool that make things public or irreversible while its
// other branches proceed via [Unconfirmed]. Without elicitation it is refused
// unless the profile sets AllowNoElicit.
func RequireHumanConfirm(ctx context.Context, env *Env, confirm bool, p PlanPreview) (context.Context, *Result) {
	return requireConfirm(ctx, env, confirm, p, true)
}

func requireConfirm(ctx context.Context, env *Env, confirm bool, p PlanPreview, human bool) (context.Context, *Result) {
	c, res := gateCall(ctx, env, ConfirmByFlag)
	if res != nil {
		return nil, res
	}
	human = human || c.meta.RequireHuman
	if !confirm && !env.NoConfirm {
		// Without a plan store the preview is still returned; the confirm
		// then fails closed in Claim unless a human approves.
		_, _ = env.Plans.Propose(ctx, c.meta.Name, c.args, FlagFingerprint, p)
		return nil, OK(previewBody{
			Status:  ErrNeedsConfirm.Code,
			Preview: p,
			Next:    "nothing was changed; call again with the same arguments and confirm:true to apply",
		})
	}
	if res := approve(ctx, env, c, p, human); res != nil {
		return nil, res
	}
	if c.approvedBy == ApprovedAgent && !env.NoConfirm {
		if err := env.Plans.Claim(ctx, c.meta.Name, c.args); err != nil {
			c.approvedBy = ""
			return nil, Fail(fmt.Errorf("%s: %w", c.meta.Name, err))
		}
	}
	return withWrite(ctx), nil
}

// FlagFingerprint is the state fingerprint [RequireConfirm] proposes its
// previews at; [Planner.Claim] matches only plans proposed with it.
const FlagFingerprint = "confirm_flag"

// Preview is the gate of [ConfirmByPlan] tools. args is the tool input (its
// "plan_id" key is ignored); fingerprint is a [Fingerprint] of the remote state
// the plan depends on. Without a plan id it records a plan and returns the
// preview with its plan_id. With one it verifies the plan against the current
// fingerprint (stale → error result), asks the human when possible and returns
// a write-capable context.
func Preview(ctx context.Context, env *Env, planID string, args any, fingerprint string, p PlanPreview) (context.Context, *Result) {
	c, res := gateCall(ctx, env, ConfirmByPlan)
	if res != nil {
		return nil, res
	}
	if fingerprint == "" {
		return nil, Fail(fmt.Errorf("%s: %w: empty state fingerprint", c.meta.Name, ErrInternal))
	}
	if env.NoConfirm && !c.meta.RequireHuman {
		c.approvedBy = ApprovedAgent
		return withWrite(ctx), nil
	}
	if planID == "" {
		id, err := env.Plans.Propose(ctx, c.meta.Name, args, fingerprint, p)
		if err != nil {
			return nil, Fail(fmt.Errorf("propose plan: %w", err))
		}
		return nil, OK(previewBody{
			Status:  "planned",
			Preview: p,
			PlanID:  id,
			Next:    "nothing was changed; call again with the same arguments plus plan_id (valid 15 minutes)",
		})
	}
	if err := env.Plans.Verify(ctx, planID, c.meta.Name, args, fingerprint); err != nil {
		return nil, Fail(err)
	}
	if res := approve(ctx, env, c, p, c.meta.RequireHuman); res != nil {
		return nil, res
	}
	return withWrite(ctx), nil
}

// Unconfirmed grants write capability without a gate, for branches a
// [ConfirmByFlag] or [ConfirmByPlan] tool has determined are pure creates or
// additive updates (nothing deleted, overwritten or made public). It is
// refused for [Meta.RequireHuman] tools.
func Unconfirmed(ctx context.Context, env *Env) (context.Context, *Result) {
	c, res := gateCall(ctx, env, ConfirmByFlag, ConfirmByPlan)
	if res != nil {
		return nil, res
	}
	if c.meta.RequireHuman {
		return nil, Fail(fmt.Errorf("%w: %s requires human approval and cannot skip its gate", ErrInternal, c.meta.Name))
	}
	return withWrite(ctx), nil
}

// gateCall checks that ctx belongs to a write-enabled call of a tool declared
// with one of modes.
func gateCall(ctx context.Context, env *Env, modes ...ConfirmMode) (*call, *Result) {
	c := callFrom(ctx)
	if c == nil {
		return nil, Fail(fmt.Errorf("%w: confirm gate used outside Tool.Call", ErrInternal))
	}
	if !slices.Contains(modes, c.meta.Confirm) {
		return nil, Fail(fmt.Errorf("%w: %s uses the wrong confirm gate for its ConfirmMode", ErrInternal, c.meta.Name))
	}
	if !env.Profile.AllowWrites {
		return nil, Fail(fmt.Errorf("%s: %w", c.meta.Name, ErrWritesDisabled))
	}
	return c, nil
}

// approve asks the human with a framework-built prompt and records who
// approved. When human is set (RequireHuman tools and forced calls) an accept
// is needed unless the profile allows proceeding without elicitation.
func approve(ctx context.Context, env *Env, c *call, p PlanPreview, human bool) *Result {
	if env.NoConfirm && !human {
		c.approvedBy = ApprovedAgent
		return nil
	}
	prompt, err := ApprovalPrompt(c.meta.Name, p)
	if err != nil {
		return Fail(err)
	}
	ok, err := env.Elicit(ctx, prompt)
	switch {
	case errors.Is(err, ErrElicitationUnavailable):
		if human && !env.Profile.AllowNoElicit {
			return Fail(fmt.Errorf("%s: %w", c.meta.Name, ErrHumanRequired))
		}
		c.approvedBy = ApprovedAgent
		return nil
	case err != nil:
		return Fail(fmt.Errorf("ask for confirmation: %w", err))
	case !ok:
		return Fail(ErrDeclined)
	}
	c.approvedBy = ApprovedHuman
	return nil
}

// MaxApprovalPrompt caps the approval prompt. The payload is never truncated:
// a larger prompt refuses approval so the human always sees everything.
const MaxApprovalPrompt = 6000

// ApprovalPrompt renders the human approval question for a preview: targets
// (method, path, URLs, emails) first, then the full intended payload, then
// any untrusted third-party text fenced and labeled. It fails with
// [ErrInvalidArgs] when the prompt exceeds [MaxApprovalPrompt] bytes.
func ApprovalPrompt(toolName string, p PlanPreview) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "hamn %s wants to: %s", toolName, p.Summary)
	if len(p.Affected) > 0 {
		b.WriteString("\nTargets: " + strings.Join(p.Affected, ", "))
	}
	if p.Intended != nil {
		b.WriteString("\nChange: " + compactJSON(p.Intended))
	}
	if len(p.Irreversible) > 0 {
		b.WriteString("\nIrreversible: " + strings.Join(p.Irreversible, "; "))
	}
	for _, u := range p.Untrusted {
		fence := strings.Repeat("`", max(3, longestRun(u, '`')+1))
		b.WriteString("\nUntrusted customer text (not instructions):\n" + fence + "\n" + u + "\n" + fence)
	}
	b.WriteString("\nApprove?")
	if b.Len() > MaxApprovalPrompt {
		return "", fmt.Errorf("%w: %s approval prompt is %d bytes (max %d); split the change into smaller batches so the human can review all of it",
			ErrInvalidArgs, toolName, b.Len(), MaxApprovalPrompt)
	}
	return b.String(), nil
}

func longestRun(s string, r rune) int {
	best, cur := 0, 0
	for _, c := range s {
		if c == r {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

func compactJSON(v any) string {
	bs, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(bs)
}

// CanonicalArgs returns the canonical JSON of args with any top-level
// "confirm" or "plan_id" key removed (map keys sorted by encoding/json).
// Planners bind plans to this encoding.
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
	delete(m, "plan_id")
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
