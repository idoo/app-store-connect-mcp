package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Deps configures one MCP server (one per client session).
type Deps struct {
	// Tools to expose; nil means every registered tool ([tool.All]).
	Tools []tool.Tool
	// Env is the base environment. Plans, Toolsets, Profile, Version,
	// Progress and Elicit are supplied per session and per request.
	Env tool.Env
	// Profile is the active profile; Profile.AllowWrites is the start mode.
	Profile tool.ProfileInfo
	// WritesLocked forbids toolsets enable_writes (e.g. a .app-store-connect-mcp.toml pin with
	// allow_writes=false).
	WritesLocked bool
	// Toolsets enabled at start; nil means [tool.DefaultToolsets].
	Toolsets []tool.Toolset
	// Discovery starts with only the core toolset listed.
	Discovery bool
	// Version is reported in serverInfo and to tools.
	Version string
	// Logger receives SDK and server logs (never stdout); nil discards.
	Logger *slog.Logger
	// MaxResultBytes caps result payloads; 0 means
	// asc.DefaultMaxResponseBytes and a negative value disables the cap.
	MaxResultBytes int
	// Now is the plan clock; nil means time.Now.
	Now func() time.Time
}

// session is the per-server state: enabled toolsets, write mode and plans.
type session struct {
	deps  Deps
	srv   *mcp.Server
	tools []tool.Tool
	plans *Planner

	syncMu sync.Mutex // serializes sync against the SDK
	mu     sync.Mutex
	on     map[tool.Toolset]bool
	writes bool
	listed map[string]bool
	mode   bool // writes mode the listed tools were registered with
}

var _ tool.Toolsets = (*session)(nil)

// Build creates an MCP server for one client session.
func Build(_ context.Context, deps Deps) (*mcp.Server, error) {
	s, err := newSession(deps)
	if err != nil {
		return nil, err
	}
	return s.srv, nil
}

func newSession(deps Deps) (*session, error) {
	if deps.Tools == nil {
		deps.Tools = tool.All()
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.MaxResultBytes == 0 {
		deps.MaxResultBytes = asc.DefaultMaxResponseBytes
	}
	initial := deps.Toolsets
	if initial == nil {
		initial = tool.DefaultToolsets
	}
	if deps.Discovery {
		initial = []tool.Toolset{tool.Core}
	}
	s := &session{
		deps:   deps,
		plans:  NewPlanner(deps.Now),
		on:     map[tool.Toolset]bool{tool.Core: true},
		writes: deps.Profile.AllowWrites,
		listed: map[string]bool{},
	}
	for _, ts := range initial {
		if !ts.Valid() {
			return nil, fmt.Errorf("mcpserver: unknown toolset %q", ts)
		}
		s.on[ts] = true
	}
	seen := map[string]bool{}
	for _, t := range append(slices.Clone(deps.Tools), s.toolsetsTool()) {
		if seen[t.Name()] {
			return nil, fmt.Errorf("mcpserver: duplicate tool %q", t.Name())
		}
		seen[t.Name()] = true
		s.tools = append(s.tools, t)
	}
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "app-store-connect-mcp", Title: "app-store-connect-mcp", Version: deps.Version}, &mcp.ServerOptions{
		Instructions:              Instructions,
		Logger:                    deps.Logger,
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		SupportedProtocolVersions: legacyProtocolVersions(),
	})
	s.srv.AddReceivingMiddleware(s.hiddenToolMiddleware)
	s.addResources()
	s.addPrompts()
	s.sync()
	return s, nil
}

func (s *session) profile() tool.ProfileInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.deps.Profile
	p.AllowWrites = s.writes
	return p
}

// sync makes the server's tool list match the listing policy. The SDK sends
// notifications/tools/list_changed for every change.
func (s *session) sync() {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	p := s.profile()
	s.mu.Lock()
	on := maps.Clone(s.on)
	modeChanged := s.mode != p.AllowWrites
	s.mode = p.AllowWrites
	old := s.listed
	s.mu.Unlock()

	want := map[string]bool{}
	var remove []string
	for _, t := range s.tools {
		if on[t.Meta().Toolset] && t.ListedIn(p) {
			want[t.Name()] = true
			if !old[t.Name()] || modeChanged {
				s.srv.AddTool(s.mcpTool(t, p.AllowWrites), s.handler(t))
			}
		} else if old[t.Name()] {
			remove = append(remove, t.Name())
		}
	}
	if len(remove) > 0 {
		s.srv.RemoveTools(remove...)
	}
	s.mu.Lock()
	s.listed = want
	s.mu.Unlock()
}

func (s *session) mcpTool(t tool.Tool, writes bool) *mcp.Tool {
	a := t.Meta().Annotations()
	return &mcp.Tool{
		Name:        t.Name(),
		Title:       t.Meta().Title,
		Description: t.DescriptionFor(writes),
		InputSchema: flatSchema(t.InputSchemaFor(writes)),
		Annotations: &mcp.ToolAnnotations{
			Title:           a.Title,
			ReadOnlyHint:    a.ReadOnlyHint,
			DestructiveHint: a.DestructiveHint,
			IdempotentHint:  a.IdempotentHint,
			OpenWorldHint:   a.OpenWorldHint,
		},
	}
}

// legacyProtocolVersions is every SDK protocol version older than
// mrtrProtocol. On 2026-07-28 a server may no longer send elicitation/create
// mid-request, which is how app-store-connect-mcp asks the human to approve, so app-store-connect-mcp caps
// sessions at 2025-11-25: a 2026-07-28 client is told the supported list and
// falls back to the initialize handshake at 2025-11-25.
func legacyProtocolVersions() []string {
	return slices.DeleteFunc(mcp.SupportedProtocolVersions(), func(v string) bool { return v >= mrtrProtocol })
}

// flatSchema returns a copy of in where every nullable type array such as
// ["null","array"] (inferred from Go nil slices and pointers) is a single
// type. Gemini and other OpenAPI-subset clients reject type arrays; the
// fields stay optional because they are not in "required".
func flatSchema(in *jsonschema.Schema) *jsonschema.Schema {
	if in == nil {
		return nil
	}
	out := in.CloneSchemas()
	collapseNullable(out)
	return out
}

func collapseNullable(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) > 0 {
		types := slices.DeleteFunc(slices.Clone(s.Types), func(t string) bool { return t == "null" })
		if len(types) == 1 {
			s.Type, s.Types = types[0], nil
		} else {
			s.Types = types
		}
	}
	for _, c := range []*jsonschema.Schema{s.Items, s.AdditionalProperties, s.Not, s.Contains, s.PropertyNames, s.UnevaluatedItems, s.UnevaluatedProperties} {
		collapseNullable(c)
	}
	for _, list := range [][]*jsonschema.Schema{s.PrefixItems, s.AllOf, s.AnyOf, s.OneOf} {
		for _, c := range list {
			collapseNullable(c)
		}
	}
	for _, m := range []map[string]*jsonschema.Schema{s.Properties, s.PatternProperties, s.Defs, s.Definitions} {
		for _, c := range m {
			collapseNullable(c)
		}
	}
}

// List reports every toolset with its visible and withheld tool counts.
func (s *session) List() []tool.ToolsetInfo {
	p := s.profile()
	s.mu.Lock()
	on := maps.Clone(s.on)
	s.mu.Unlock()
	out := make([]tool.ToolsetInfo, 0, len(tool.AllToolsets))
	for _, ts := range tool.AllToolsets {
		info := tool.ToolsetInfo{Name: ts, Enabled: on[ts], Default: ts.IsDefault()}
		for _, t := range s.tools {
			if t.Meta().Toolset != ts {
				continue
			}
			if t.ListedIn(p) {
				info.Tools++
			} else {
				info.Withheld++
			}
		}
		out = append(out, info)
	}
	return out
}

// Enable turns toolsets on and re-lists tools.
func (s *session) Enable(_ context.Context, names ...tool.Toolset) error {
	return s.setToolsets(names, true)
}

// Disable turns toolsets off and re-lists tools; core cannot be disabled.
func (s *session) Disable(_ context.Context, names ...tool.Toolset) error {
	return s.setToolsets(names, false)
}

func (s *session) setToolsets(names []tool.Toolset, on bool) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: no toolsets given", tool.ErrInvalidArgs)
	}
	for _, n := range names {
		if !n.Valid() {
			return fmt.Errorf("%w: unknown toolset %q", tool.ErrInvalidArgs, n)
		}
		if n == tool.Core && !on {
			return fmt.Errorf("%w: the core toolset cannot be disabled", tool.ErrInvalidArgs)
		}
	}
	s.mu.Lock()
	for _, n := range names {
		s.on[n] = on
	}
	s.mu.Unlock()
	s.sync()
	return nil
}

// enableWrites switches the session to write mode and re-lists tools.
func (s *session) enableWrites() {
	s.mu.Lock()
	s.writes = true
	s.mu.Unlock()
	s.sync()
}

var errWritesLocked = &tool.Error{
	Code:    "writes_locked",
	Message: "writes are pinned off for this project or profile",
	Hint:    "writes are pinned off for this project or profile; remove allow_writes=false or add the pin's directory to trusted_pins, then restart app-store-connect-mcp",
}

// isElicitUnavailable reports whether err means the client cannot elicit.
func isElicitUnavailable(err error) bool {
	return errors.Is(err, tool.ErrElicitationUnavailable)
}
