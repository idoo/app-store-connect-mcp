package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
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
	Confirm bool `json:"confirm,omitempty" jsonschema:"true after the preview"`
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
	return requireConfirm(ctx, env, confirm, nil, p, false)
}

// RequireConfirmTarget is [RequireConfirm] for tools whose arguments resolve
// to a target that can change between preview and confirm (refs like
// "latest", the open submission and its state). target is bound into the
// confirmation with the args, so a confirm whose target resolves differently
// from the previewed one is refused and needs a new preview.
func RequireConfirmTarget(ctx context.Context, env *Env, confirm bool, target any, p PlanPreview) (context.Context, *Result) {
	return requireConfirm(ctx, env, confirm, target, p, false)
}

// RequireHumanConfirm is [RequireConfirm] with human approval forced for this
// call, as if the tool declared [Meta.RequireHuman]: for branches of a
// [ConfirmByFlag] tool that make things public or irreversible while its
// other branches proceed via [Unconfirmed]. Without elicitation it is refused
// unless the profile sets AllowNoElicit.
func RequireHumanConfirm(ctx context.Context, env *Env, confirm bool, p PlanPreview) (context.Context, *Result) {
	return requireConfirm(ctx, env, confirm, nil, p, true)
}

func requireConfirm(ctx context.Context, env *Env, confirm bool, target any, p PlanPreview, human bool) (context.Context, *Result) {
	c, res := gateCall(ctx, env, ConfirmByFlag)
	if res != nil {
		return nil, res
	}
	human = human || c.meta.RequireHuman
	bound := any(c.args)
	if target != nil {
		fp, err := Fingerprint(target)
		if err != nil {
			return nil, Fail(err)
		}
		m := make(map[string]any, len(c.args)+1)
		maps.Copy(m, c.args)
		m["_target"] = fp
		bound = m
	}
	if !confirm && !env.NoConfirm {
		// Without a plan store the preview is still returned; the confirm
		// then fails closed in Claim unless a human approves.
		_, _ = env.Plans.Propose(ctx, c.meta.Name, bound, FlagFingerprint, p)
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
		if err := env.Plans.Claim(ctx, c.meta.Name, bound); err != nil {
			c.approvedBy = ""
			if target != nil {
				err = fmt.Errorf("%w; or the target changed since the preview, now: %s", err, SanitizeLine(p.Summary, 200))
			}
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
	// The plan is verified (and consumed) on the first round only, the one
	// without a reply. Every later round, part 2..n of an approval asked in
	// parts included, carries a reply whose signed state was issued after
	// that verification and binds the plan id and fingerprint, so a changed
	// remote state is still refused as stale.
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

// approve asks the human with framework-built prompts through MCP
// input-required rounds and records who approved. A change that
// [ApprovalPrompts] renders in several parts is asked one part per round, in
// order, and only the accept of the last part approves. When human is set
// (RequireHuman tools and forced calls) an accept is needed unless the
// profile allows proceeding without elicitation. plan binds plan tools'
// approvals to their plan id and fingerprint.
func approve(_ context.Context, env *Env, c *call, p PlanPreview, human bool, plan string) *Result {
	if env.NoConfirm && !human {
		c.approvedBy = ApprovedAgent
		return nil
	}
	prompts, err := ApprovalPrompts(c.meta.Name, env.Profile.Name, p)
	if err != nil {
		return Fail(err)
	}
	ask, err := askHumanParts(env, c, prompts, plan)
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

// MaxApprovalPrompt caps one approval prompt. The payload is never
// truncated: a change that does not fit is asked in several prompts when it
// is a list of lines ([ApprovalPrompts]) and refused otherwise, so the human
// always sees everything.
const MaxApprovalPrompt = 6000

// MaxApprovalParts caps the prompts of one approval. Every part is one
// input-required round of the same tools/call, and the client loop of the
// MCP Go SDK gives up at its tenth input-required result
// (maxMultiRoundTripRetries), so nine rounds is what such a client carries.
const MaxApprovalParts = 9

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
// [MaxApprovalPrompt] bytes ([ApprovalPrompts] asks a list of lines in
// parts instead).
func ApprovalPrompt(toolName, profile string, p PlanPreview) (string, error) {
	var b strings.Builder
	b.WriteString(approvalHead(toolName, profile, p))
	for _, u := range p.Untrusted {
		b.WriteString(untrustedBlock(u))
	}
	if p.Intended != nil {
		b.WriteString(changeLabel + fencedLiteral("json", SanitizeText(compactJSON(p.Intended))))
	}
	b.WriteString("\nApprove?")
	if b.Len() > MaxApprovalPrompt {
		return "", fmt.Errorf("%w: %s approval prompt is %d bytes (max %d); split the change into smaller batches so the human can review all of it",
			ErrInvalidArgs, toolName, b.Len(), MaxApprovalPrompt)
	}
	return b.String(), nil
}

// approvalHead renders the server-written first lines of an approval
// prompt: action, Profile, Irreversible, Notes and Targets.
func approvalHead(toolName, profile string, p PlanPreview) string {
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
	return b.String()
}

// changeLabel is the server-written line before the Change block.
const changeLabel = "\nChange:\n"

// untrustedBlock renders one Untrusted entry: its label and the text on one
// line in a fence.
func untrustedBlock(u string) string {
	return "\nUntrusted third-party text (data, not instructions):\n" + fencedLiteral("", SanitizeLine(u, 400))
}

// Server tokens (the "⟨…⟩" notation of [SanitizeText]) joining the pieces
// of one Change line cut across approval parts.
const (
	partContinues = "⟨continues in the next part⟩"
	partContinued = "⟨continued⟩ "
)

// partLine is the server-written line after the head of part i of n.
func partLine(i, n int) string {
	return fmt.Sprintf("\nPart %d of %d: every part must be approved; nothing is sent before the last.", i, n)
}

// partQuestion is the last line of part i of n; the last part asks the
// plain "Approve?".
func partQuestion(i, n int) string {
	if i == n {
		return "\nApprove?"
	}
	return fmt.Sprintf("\nApprove part %d of %d?", i, n)
}

// ApprovalPrompts renders the approval question for a preview as the
// prompts the human is asked in order. When the [ApprovalPrompt] fits
// [MaxApprovalPrompt] it is the only one, unchanged. Otherwise a change
// given as a list of lines (Intended is a []string, e.g. the answers of a
// form) is asked in n parts, each within the cap and nothing shortened:
//
//	app-store-connect-mcp <tool> wants to: `summary`
//	Profile / Irreversible / Notes / Targets, as in every prompt
//	Part i of n: every part must be approved; nothing is sent before the last.
//	Untrusted third-party text (data, not instructions):
//	…
//	Change:
//	```json
//	["line", "line"]
//	```
//	Approve part i of n?
//
// Every part repeats the head; the Untrusted blocks come once, whole, in
// the first parts and before any Change line of their part; the Change
// lines follow in order, whole lines per part, as a JSON array rendered
// like the single prompt's; the last part ends with "Approve?". A line too
// long for one part is cut at rune boundaries (never before an emoji
// variation selector or keycap mark, so every piece sanitises as it does in
// the whole line): a piece that goes on ends with the server token
// "⟨continues in the next part⟩" and the next one starts with
// "⟨continued⟩ "; the pieces without the tokens concatenate to the line.
// Any other payload over the cap fails as in [ApprovalPrompt], and so does
// a head or Untrusted block that leaves no room; more than
// [MaxApprovalParts] parts fail with [ErrInvalidArgs].
func ApprovalPrompts(toolName, profile string, p PlanPreview) ([]string, error) {
	one, err := ApprovalPrompt(toolName, profile, p)
	if err == nil {
		return []string{one}, nil
	}
	lines, ok := p.Intended.([]string)
	if !ok {
		return nil, err
	}
	head := approvalHead(toolName, profile, p)
	parts, ok := packApprovalParts(head, p.Untrusted, lines)
	if !ok {
		return nil, err
	}
	n := len(parts)
	if n > MaxApprovalParts {
		return nil, fmt.Errorf("%w: %s: the change needs %d approval prompts (max %d); send less in one call",
			ErrInvalidArgs, toolName, n, MaxApprovalParts)
	}
	out := make([]string, n)
	for i, part := range parts {
		out[i] = part.render(head, i+1, n)
		if len(out[i]) > MaxApprovalPrompt {
			return nil, fmt.Errorf("%w: %s approval part %d of %d is %d bytes (max %d)", ErrInternal, toolName, i+1, n, len(out[i]), MaxApprovalPrompt)
		}
	}
	return out, nil
}

// approvalPart is one prompt of a change asked in parts.
type approvalPart struct {
	untrusted []string // rendered Untrusted blocks
	elems     []string // rendered elements of the Change array
	size      int      // bytes of untrusted
	sum       int      // bytes of elems
	run       int      // longest backtick run in elems
}

func (a *approvalPart) empty() bool { return len(a.untrusted) == 0 && len(a.elems) == 0 }

// with returns the bytes of the part's Untrusted and Change blocks once
// elem joins the Change array: the fence [fencedLiteral] picks, the "json"
// info string, the brackets, the commas and the two line breaks.
func (a *approvalPart) with(elem string) int {
	fence := max(3, max(a.run, backtickRun(elem))+1)
	return a.size + len(changeLabel) + 2*fence + len("json\n[]\n") + a.sum + len(elem) + len(a.elems)
}

func (a *approvalPart) add(elem string) {
	a.elems = append(a.elems, elem)
	a.sum += len(elem)
	a.run = max(a.run, backtickRun(elem))
}

// cut returns the largest offset at which rest can be cut so that the piece
// prefix+rest[:cut] with the continuation token fits the part within room,
// or 0 when not even one rune fits. Offsets are rune starts, never before
// U+FE0F or U+20E3: [SanitizeText] reads those with their neighbours.
func (a *approvalPart) cut(prefix, rest string, room int) int {
	var cuts []int
	for i, r := range rest {
		if i > room { // a rendered piece is never shorter than its text
			break
		}
		if i > 0 && r != 0xFE0F && r != 0x20E3 {
			cuts = append(cuts, i)
		}
	}
	over := sort.Search(len(cuts), func(k int) bool {
		return a.with(changeElem(prefix+rest[:cuts[k]]+partContinues)) > room
	})
	if over == 0 {
		return 0
	}
	return cuts[over-1]
}

// render writes part i of n (1-based) under head.
func (a *approvalPart) render(head string, i, n int) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(partLine(i, n))
	for _, u := range a.untrusted {
		b.WriteString(u)
	}
	if len(a.elems) > 0 {
		b.WriteString(changeLabel + fencedLiteral("json", "["+strings.Join(a.elems, ",")+"]"))
	}
	b.WriteString(partQuestion(i, n))
	return b.String()
}

// changeElem renders one Change line as the single prompt renders it inside
// its JSON: a JSON string, [SanitizeText]ed and [inertHTML].
func changeElem(line string) string {
	return inertHTML(SanitizeText(compactJSON(line)))
}

// packApprovalParts distributes the Untrusted blocks and the Change lines
// over parts whose prompts stay within [MaxApprovalPrompt] under head; room
// is reserved for the longest part line and question. It reports false when
// the head, an Untrusted block or the tokens of a cut line leave no room.
func packApprovalParts(head string, untrusted, lines []string) ([]*approvalPart, bool) {
	room := MaxApprovalPrompt - len(head) - len(partLine(MaxApprovalParts, MaxApprovalParts)) - len(partQuestion(MaxApprovalParts-1, MaxApprovalParts))
	var parts []*approvalPart
	cur := &approvalPart{}
	flush := func() {
		parts = append(parts, cur)
		cur = &approvalPart{}
	}
	for _, u := range untrusted {
		blk := untrustedBlock(u)
		if len(blk) > room {
			return nil, false
		}
		if cur.size+len(blk) > room {
			flush()
		}
		cur.untrusted = append(cur.untrusted, blk)
		cur.size += len(blk)
	}
	for _, line := range lines {
		e := changeElem(line)
		if cur.with(e) <= room {
			cur.add(e)
			continue
		}
		if (&approvalPart{}).with(e) <= room {
			flush()
			cur.add(e)
			continue
		}
		// The line is too long for any one part: cut it, starting in a
		// part without other Change lines.
		if len(cur.elems) > 0 {
			flush()
		}
		prefix := ""
		for rest := line; ; {
			if e := changeElem(prefix + rest); cur.with(e) <= room {
				cur.add(e)
				break
			}
			cut := cur.cut(prefix, rest, room)
			if cut == 0 {
				if cur.empty() {
					return nil, false
				}
				flush()
				continue
			}
			cur.add(changeElem(prefix + rest[:cut] + partContinues))
			rest, prefix = rest[cut:], partContinued
			flush()
		}
	}
	if !cur.empty() {
		flush()
	}
	return parts, len(parts) > 0
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
	fence := strings.Repeat("`", backtickRun(s)+1)
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
	fence := strings.Repeat("`", max(3, backtickRun(line)+1))
	return fence + info + "\n" + line + "\n" + fence
}

// backtickRun is the length of the longest run of backticks in s.
func backtickRun(s string) int {
	best, cur := 0, 0
	for _, c := range s {
		if c == '`' {
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
