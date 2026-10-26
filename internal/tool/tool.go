package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// MaxDescription is the maximum tool description length in bytes.
const MaxDescription = 280

// RateLimitWarnBelow is the remaining-call threshold that adds a warning.
const RateLimitWarnBelow = 200

// Meta is the static declaration of a tool.
type Meta struct {
	// Name is the snake_case MCP tool name (unique).
	Name    string
	Toolset Toolset
	Title   string
	// Description is at most MaxDescription bytes; enums belong in the schema.
	Description string
	// Class is R, W or D; it sets the annotations, read-only withholding and
	// must match the name's verb (see TestRegistryNamingAndMeta).
	Class Class
	// Idempotent sets idempotentHint: repeating the call with the same input
	// has no further effect. Every Read tool must set it, except cursor-writing
	// reads (mark_seen).
	Idempotent bool
	// Local tools never call Apple or the network (openWorldHint=false).
	Local bool
	// NeedsTeamKey tools are withheld and refused for Individual API keys.
	NeedsTeamKey bool
	// AvailableReadOnly keeps a Write/Destructive tool listed in read-only
	// mode; the tool restricts itself (e.g. asc_api_request becomes GET-only via
	// ReadOnlyEnums) and receives no write capability.
	AvailableReadOnly bool
	// Confirm is the gate mode: ConfirmByPlan (plan_id from a preview call) or
	// ConfirmByFlag (confirm:true after a preview); zero means no gate.
	Confirm ConfirmMode
	// RequireHuman makes the confirm gate demand a human accept via
	// elicitation (public, irreversible or money actions). Without
	// elicitation the call is refused with [ErrHumanRequired] unless the
	// profile sets AllowNoElicit. Requires a Confirm mode.
	RequireHuman bool
	// Enums adds enum constraints to input properties; keys are property paths
	// with "." between levels (array items are traversed implicitly).
	Enums map[string][]any
	// ReadOnlyEnums replaces enums in read-only mode (schema and validation).
	ReadOnlyEnums map[string][]any
	// ReadOnlyDescription replaces Description in read-only mode.
	ReadOnlyDescription string
}

// Handler is a typed tool implementation. A returned error becomes an error
// result via [Fail]; returning (nil, nil) yields {"ok":true}.
type Handler[In any] func(ctx context.Context, env *Env, in In) (*Result, error)

// Tool is a registered, type-erased tool.
type Tool struct {
	meta       Meta
	schema     *jsonschema.Schema
	resolved   *jsonschema.Resolved
	roSchema   *jsonschema.Schema
	roResolved *jsonschema.Resolved
	invoke     func(ctx context.Context, env *Env, args map[string]any) (*Result, error)
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// New builds a tool from its metadata and typed handler. The input schema is
// inferred from In (exported fields, json names, `jsonschema:"description"`
// tags; fields without omitempty are required). New panics on declaration
// errors; it is meant to be called from package init via [Register].
func New[In any](m Meta, h Handler[In]) Tool {
	if err := m.check(); err != nil {
		panic(fmt.Sprintf("tool %q: %v", m.Name, err))
	}
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %q: infer schema: %v", m.Name, err))
	}
	if s.Type != "object" {
		panic(fmt.Sprintf("tool %q: input must be a struct, got schema type %q", m.Name, s.Type))
	}
	if err := checkConfirmField(m.Confirm, s); err != nil {
		panic(fmt.Sprintf("tool %q: %v", m.Name, err))
	}
	if err := applyEnums(s, m.Enums); err != nil {
		panic(fmt.Sprintf("tool %q: %v", m.Name, err))
	}
	t := Tool{meta: m, schema: s}
	if t.resolved, err = s.Resolve(nil); err != nil {
		panic(fmt.Sprintf("tool %q: resolve schema: %v", m.Name, err))
	}
	t.roSchema, t.roResolved = s, t.resolved
	if len(m.ReadOnlyEnums) > 0 {
		t.roSchema = s.CloneSchemas()
		if err := applyEnums(t.roSchema, m.ReadOnlyEnums); err != nil {
			panic(fmt.Sprintf("tool %q: read-only enums: %v", m.Name, err))
		}
		if t.roResolved, err = t.roSchema.Resolve(nil); err != nil {
			panic(fmt.Sprintf("tool %q: resolve read-only schema: %v", m.Name, err))
		}
	}
	t.invoke = func(ctx context.Context, env *Env, args map[string]any) (*Result, error) {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
		}
		var in In
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidArgs, err)
		}
		return h(ctx, env, in)
	}
	return t
}

func (m Meta) check() error {
	switch {
	case !nameRE.MatchString(m.Name):
		return fmt.Errorf("name must match %s", nameRE)
	case !m.Toolset.Valid():
		return fmt.Errorf("unknown toolset %q", m.Toolset)
	case !m.Class.Valid():
		return fmt.Errorf("class not set")
	case m.Description == "":
		return fmt.Errorf("description is empty")
	case len(m.Description) > MaxDescription || len(m.ReadOnlyDescription) > MaxDescription:
		return fmt.Errorf("description longer than %d bytes", MaxDescription)
	case m.Confirm != ConfirmNone && m.Class == Read:
		return fmt.Errorf("read tools cannot require confirmation")
	case m.Class == Destructive && m.Confirm == ConfirmNone && !m.AvailableReadOnly:
		return fmt.Errorf("destructive tools must use ConfirmByFlag or ConfirmByPlan")
	case m.RequireHuman && m.Confirm == ConfirmNone:
		return fmt.Errorf("RequireHuman needs ConfirmByFlag or ConfirmByPlan")
	}
	return nil
}

func checkConfirmField(mode ConfirmMode, s *jsonschema.Schema) error {
	switch mode {
	case ConfirmByFlag:
		if p := s.Properties["confirm"]; p == nil || p.Type != "boolean" {
			return fmt.Errorf("ConfirmByFlag tools must embed tool.ConfirmFlag")
		}
	case ConfirmByPlan:
		if p := s.Properties["plan_id"]; p == nil || p.Type != "string" {
			return fmt.Errorf("ConfirmByPlan tools must embed tool.ConfirmPlan")
		}
	}
	return nil
}

func applyEnums(s *jsonschema.Schema, enums map[string][]any) error {
	for path, values := range enums {
		cur := s
		for seg := range strings.SplitSeq(path, ".") {
			for cur.Items != nil && cur.Properties == nil {
				cur = cur.Items
			}
			next := cur.Properties[seg]
			if next == nil {
				return fmt.Errorf("enum path %q: no property %q", path, seg)
			}
			cur = next
		}
		for cur.Items != nil && (cur.Type == "array" || slices.Contains(cur.Types, "array")) {
			cur = cur.Items
		}
		cur.Enum = slices.Clone(values)
	}
	return nil
}

// Meta returns the tool's declaration.
func (t Tool) Meta() Meta { return t.meta }

// Name returns the tool name.
func (t Tool) Name() string { return t.meta.Name }

// InputSchema returns the full input schema (do not mutate).
func (t Tool) InputSchema() *jsonschema.Schema { return t.schema }

// InputSchemaFor returns the schema advertised in the given mode.
func (t Tool) InputSchemaFor(allowWrites bool) *jsonschema.Schema {
	if allowWrites {
		return t.schema
	}
	return t.roSchema
}

// DescriptionFor returns the description advertised in the given mode.
func (t Tool) DescriptionFor(allowWrites bool) string {
	if !allowWrites && t.meta.ReadOnlyDescription != "" {
		return t.meta.ReadOnlyDescription
	}
	return t.meta.Description
}

// ListedIn reports whether the tool is offered for the given profile mode.
func (t Tool) ListedIn(p ProfileInfo) bool {
	if t.meta.NeedsTeamKey && p.KeyKind == KeyIndividual {
		return false
	}
	return t.meta.Class == Read || p.AllowWrites || t.meta.AvailableReadOnly
}

// Call runs the tool with raw JSON arguments. It enforces the write and key
// guards, substitutes stand-ins for nil Env members, decodes and validates
// arguments (accepting stringified JSON for object/array fields), recovers
// panics as internal_error results and converts errors to error results.
// Write capability is granted up front only to ConfirmNone Write tools; gated
// tools obtain it from their confirm gate. It never returns nil.
func (t Tool) Call(ctx context.Context, env *Env, raw json.RawMessage) (res *Result) {
	env = env.complete()
	env.Files = withRoots(ctx, env.Files, env.Roots)
	c := &call{meta: t.meta}
	defer func() {
		if p := recover(); p != nil {
			res = Fail(fmt.Errorf("%s: %w: panic: %v", t.meta.Name, ErrInternal, p))
		}
		if res == nil {
			res = OK(map[string]any{"ok": true})
		}
		res.Meta.Profile = env.Profile.Name
		res.Meta.PinSource = env.Profile.PinSource
		res.Meta.ApprovedBy = ApprovedNone
		if c.approvedBy != "" {
			res.Meta.ApprovedBy = c.approvedBy
		}
		if rl := env.ASC.RateLimit(); rl.Known {
			res.Meta.RateLimit = &rl
			if rl.Remaining < RateLimitWarnBelow {
				res.Warn(fmt.Sprintf("only %d App Store Connect calls left this hour", rl.Remaining))
			}
		}
	}()
	return t.call(withCall(ctx, c), env, raw)
}

func (t Tool) call(ctx context.Context, env *Env, raw json.RawMessage) *Result {
	m := t.meta
	if m.NeedsTeamKey && env.Profile.KeyKind == KeyIndividual {
		return Fail(fmt.Errorf("%s: %w", m.Name, ErrIndividualKeyUnsupported))
	}
	if m.Class != Read && !env.Profile.AllowWrites && !m.AvailableReadOnly {
		return Fail(fmt.Errorf("%s: %w", m.Name, ErrWritesDisabled))
	}
	if m.Class == Write && m.Confirm == ConfirmNone && env.Profile.AllowWrites {
		ctx = withWrite(ctx)
	}
	schema, resolved := t.schema, t.resolved
	if !env.Profile.AllowWrites {
		schema, resolved = t.roSchema, t.roResolved
	}
	args, err := decodeArgs(raw, schema, resolved)
	if err != nil {
		return Fail(fmt.Errorf("%w: %v", ErrInvalidArgs, err))
	}
	if c := callFrom(ctx); c != nil {
		c.args = args
	}
	res, err := t.invoke(ctx, env, args)
	if err != nil {
		return Fail(err)
	}
	return res
}

var (
	registryMu sync.Mutex
	registry   = map[string]Tool{}
)

// Register adds t to the global registry. It panics on a duplicate name and is
// meant to be called from package init.
func Register(t Tool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if t.invoke == nil {
		panic("tool.Register: tool not built with tool.New")
	}
	if _, dup := registry[t.meta.Name]; dup {
		panic(fmt.Sprintf("tool.Register: duplicate tool %q", t.meta.Name))
	}
	registry[t.meta.Name] = t
}

// All returns every registered tool sorted by name.
func All() []Tool {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Tool, 0, len(registry))
	for _, t := range registry {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Tool) int { return strings.Compare(a.meta.Name, b.meta.Name) })
	return out
}

// Lookup returns the registered tool with the given name.
func Lookup(name string) (Tool, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	t, ok := registry[name]
	return t, ok
}
