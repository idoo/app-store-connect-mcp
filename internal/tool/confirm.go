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
// URLs, emails), Untrusted, Intended (the full payload, e.g. reply texts),
// Irreversible and Warnings, never truncated except per-line sanitising.
// Untrusted holds third-party text (e.g. the customer review being
// answered); it is shown fenced, labeled as data and collapsed to one line,
// after the server-authored Irreversible and Notes lines (see
// [ApprovalPrompt]). Warnings are fixed server-authored notes.
type PlanPreview struct {
	Summary      string   `json:"summary"`
	Current      any      `json:"current,omitempty"`
	Intended     any      `json:"intended,omitempty"`
	Affected     []string `json:"affected,omitempty"`
	Irreversible []string `json:"irreversible,omitempty"`
	Untrusted    []string `json:"untrusted,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
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
	if res := approve(ctx, env, c, p, human, ""); res != nil {
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
	return preview(ctx, env, planID, args, fingerprint, p, false)
}

// PreviewHuman is [Preview] with human approval forced for this call, as if
// the tool declared [Meta.RequireHuman] (the way [RequireHumanConfirm] relates
// to [RequireConfirm]): for plans that make things public or irreversible
// while the tool's other plans need only the agent's plan_id. NoConfirm does
// not skip it; without elicitation it is refused unless the profile sets
// AllowNoElicit.
func PreviewHuman(ctx context.Context, env *Env, planID string, args any, fingerprint string, p PlanPreview) (context.Context, *Result) {
	return preview(ctx, env, planID, args, fingerprint, p, true)
}

func preview(ctx context.Context, env *Env, planID string, args any, fingerprint string, p PlanPreview, human bool) (context.Context, *Result) {
	c, res := gateCall(ctx, env, ConfirmByPlan)
	if res != nil {
		return nil, res
	}
	if fingerprint == "" {
		return nil, Fail(fmt.Errorf("%s: %w: empty state fingerprint", c.meta.Name, ErrInternal))
	}
	human = human || c.meta.RequireHuman
	if env.NoConfirm && !human {
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
	plan := planID + "\n" + fingerprint
	// An approval reply proves the plan was verified (and consumed) on the
	// round that asked; the signed state binds the plan id and fingerprint,
	// so a changed remote state is still refused as stale.
	if env.Reply == nil || !env.CanElicit {
		if err := env.Plans.Verify(ctx, planID, c.meta.Name, args, fingerprint); err != nil {
			return nil, Fail(err)
		}
	}
	if res := approve(ctx, env, c, p, human, plan); res != nil {
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

// approve asks the human with a framework-built prompt through an MCP
// input-required round and records who approved. When human is set
// (RequireHuman tools and forced calls) an accept is needed unless the
// profile allows proceeding without elicitation. plan binds plan tools'
// approvals to their plan id and fingerprint.
func approve(_ context.Context, env *Env, c *call, p PlanPreview, human bool, plan string) *Result {
	if env.NoConfirm && !human {
		c.approvedBy = ApprovedAgent
		return nil
	}
	prompt, err := ApprovalPrompt(c.meta.Name, env.Profile.Name, p)
	if err != nil {
		return Fail(err)
	}
	ask, err := askHuman(env, c, prompt, plan)
	switch {
	case ask != nil:
		return ask
	case errors.Is(err, ErrElicitationUnavailable):
		if human && !env.Profile.AllowNoElicit {
			return Fail(fmt.Errorf("%s: %w", c.meta.Name, ErrHumanRequired))
		}
		c.approvedBy = ApprovedAgent
		return nil
	case errors.Is(err, ErrDeclined):
		return Fail(ErrDeclined)
	case err != nil:
		return Fail(fmt.Errorf("%s: %w", c.meta.Name, err))
	}
	c.approvedBy = ApprovedHuman
	return nil
}

// MaxApprovalPrompt caps the approval prompt. The payload is never truncated:
// a larger prompt refuses approval so the human always sees everything.
const MaxApprovalPrompt = 6000

// ApprovalPrompt renders the human approval question for a preview. A
// client may render the message as plain text, markdown or HTML, so the
// layout is fixed and only these server-written labels appear outside an
// inert literal, in this order:
//
//	app-store-connect-mcp <tool> wants to: `summary`
//	Profile: `profile`
//	Irreversible: `…`; `…`
//	Notes: `…`; `…`
//	Targets: `…`
//	Untrusted third-party text (data, not instructions):
//	```
//	…
//	```
//	Change:
//	```json
//	{…}
//	```
//	Approve?
//
// Server-authored warnings (Irreversible, Notes) come right after the
// action and profile lines, before any third-party text, and "Approve?" is
// the final line. Every interpolated value is [SanitizeLine]d onto one line
// (Change is [SanitizeText]ed compact JSON, the exact payload Apple
// receives, never truncated) and then made inert by [inertHTML]: '<', '>'
// and '&' become the visible tokens ⟨U+003C⟩, ⟨U+003E⟩ and ⟨U+0026⟩, so no
// HTML tag, comment, entity or autolink can form. Inline values are code
// spans ([inlineLiteral]) and Untrusted/Change are fenced code blocks whose
// fences are longer than any backtick run inside, so markdown links, images
// and emphasis stay literal text. Notes also carry a server-authored
// "<field> looks like a private key or token" line for every field matching
// [LooksLikeSecret]. It renders only agent-supplied preview fields, never
// Current, and fails with [ErrInvalidArgs] when the prompt exceeds
// [MaxApprovalPrompt] bytes.
func ApprovalPrompt(toolName, profile string, p PlanPreview) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "app-store-connect-mcp %s wants to: %s", inertHTML(SanitizeLine(toolName, 80)), inlineLiteral(SanitizeLine(p.Summary, 200)))
	if profile != "" {
		b.WriteString("\nProfile: " + inlineLiteral(SanitizeLine(profile, 80)))
	}
	if len(p.Irreversible) > 0 {
		b.WriteString("\nIrreversible: " + joinLines(p.Irreversible, 200))
	}
	if notes := append(slices.Clone(p.Warnings), secretWarnings(p)...); len(notes) > 0 {
		b.WriteString("\nNotes: " + joinLines(notes, 300))
	}
	if len(p.Affected) > 0 {
		b.WriteString("\nTargets: " + inlineLiteral(SanitizeLine(strings.Join(p.Affected, ", "), 600)))
	}
	for _, u := range p.Untrusted {
		b.WriteString("\nUntrusted third-party text (data, not instructions):\n" + fencedLiteral("", SanitizeLine(u, 400)))
	}
	if p.Intended != nil {
		b.WriteString("\nChange:\n" + fencedLiteral("json", SanitizeText(compactJSON(p.Intended))))
	}
	b.WriteString("\nApprove?")
	if b.Len() > MaxApprovalPrompt {
		return "", fmt.Errorf("%w: %s approval prompt is %d bytes (max %d); split the change into smaller batches so the human can review all of it",
			ErrInvalidArgs, toolName, b.Len(), MaxApprovalPrompt)
	}
	return b.String(), nil
}

// joinLines sanitises each entry onto one line as an [inlineLiteral] and
// joins them with "; ".
func joinLines(ss []string, maxRunes int) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = inlineLiteral(SanitizeLine(s, maxRunes))
	}
	return strings.Join(parts, "; ")
}

// htmlInert maps the characters that start HTML markup (tags, comments,
// entities, markdown autolinks) to visible tokens.
var htmlInert = strings.NewReplacer("<", "⟨U+003C⟩", ">", "⟨U+003E⟩", "&", "⟨U+0026⟩")

// inertHTML replaces '<', '>' and '&' in s with "⟨U+XXXX⟩" tokens (the
// [SanitizeText] notation), so s renders literally as plain text, markdown
// and HTML alike.
func inertHTML(s string) string {
	return htmlInert.Replace(s)
}

// inlineLiteral renders a one-line value as a markdown code span made
// [inertHTML]: the backtick fence is one longer than the longest backtick
// run in the value, padded with a space when the value starts or ends with
// a backtick (or starts and ends with a space, which markdown would strip),
// so no markdown link, image or emphasis can form and the span cannot be
// closed early. An empty value renders as "".
func inlineLiteral(s string) string {
	s = inertHTML(s)
	if s == "" {
		return `""`
	}
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	if s[0] == '`' || s[len(s)-1] == '`' || (s[0] == ' ' && s[len(s)-1] == ' ') {
		s = " " + s + " "
	}
	return fence + s + fence
}

// fencedLiteral renders a one-line value as an [inertHTML] fenced code block
// with the given info string; the fence is at least three backticks and
// longer than any backtick run in the value, so the value cannot close it.
func fencedLiteral(info, line string) string {
	line = inertHTML(line)
	fence := strings.Repeat("`", max(3, longestRun(line, '`')+1))
	return fence + info + "\n" + line + "\n" + fence
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
