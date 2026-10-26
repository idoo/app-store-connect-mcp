package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type toolsetsIn struct {
	Action   string   `json:"action" jsonschema:"what to do"`
	Toolsets []string `json:"toolsets,omitempty" jsonschema:"toolsets for enable/disable"`
}

type lockedTool struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

type toolsetEntry struct {
	tool.ToolsetInfo
	Locked []lockedTool `json:"locked,omitempty"`
}

// errStatelessFixed refuses runtime toolset and write-mode changes on the
// stateless HTTP path, where every request sees the server-level config.
var errStatelessFixed = &tool.Error{
	Code:    tool.ErrUnavailable.Code,
	Message: "toolsets and write mode are fixed for stateless (2026-07-28) HTTP clients",
	Hint:    "restart app-store-connect-mcp with --toolsets and/or --allow-writes (or the matching env/profile settings)",
}

// toolsetsTool builds the session's toolsets tool (core, local). Its
// enable_writes action changes the session's security mode, so it is a Write
// tool (no readOnlyHint) that stays listed in read-only mode.
func (s *session) toolsetsTool() tool.Tool {
	names := make([]any, len(tool.AllToolsets))
	for i, ts := range tool.AllToolsets {
		names[i] = string(ts)
	}
	return tool.New(tool.Meta{
		Name:              toolsetsName,
		Toolset:           tool.Core,
		Title:             "Toolsets",
		Description:       "List toolsets, enable or disable them for this session (tool list refreshes), or ask the user to allow App Store Connect writes for this session.",
		Class:             tool.Write,
		AvailableReadOnly: true,
		Local:             true,
		Enums: map[string][]any{
			"action":   {"list", "enable", "disable", "enable_writes"},
			"toolsets": names,
		},
	}, s.toolsetsHandler)
}

func (s *session) toolsetsHandler(ctx context.Context, env *tool.Env, in toolsetsIn) (*tool.Result, error) {
	if s.deps.Stateless && in.Action != "list" {
		return nil, errStatelessFixed
	}
	switch in.Action {
	case "list":
		return tool.OK(map[string]any{"writes": env.Profile.AllowWrites, "toolsets": s.listDetailed()}), nil
	case "enable", "disable":
		ts := make([]tool.Toolset, len(in.Toolsets))
		for i, n := range in.Toolsets {
			ts[i] = tool.Toolset(n)
		}
		before := map[tool.Toolset]bool{}
		for _, info := range s.List() {
			before[info.Name] = info.Enabled
		}
		var err error
		if in.Action == "enable" {
			err = s.Enable(ctx, ts...)
		} else {
			err = s.Disable(ctx, ts...)
		}
		if err != nil {
			return nil, err
		}
		return tool.OK(map[string]any{"changed": s.changedSince(before)}), nil
	default:
		return s.enableWritesAction(ctx, env)
	}
}

func (s *session) listDetailed() []toolsetEntry {
	p := s.profile()
	infos := s.List()
	out := make([]toolsetEntry, len(infos))
	for i, info := range infos {
		out[i].ToolsetInfo = info
		for _, t := range s.tools {
			if t.Meta().Toolset != info.Name || t.ListedIn(p) {
				continue
			}
			reason := "writes_disabled"
			if t.Meta().NeedsTeamKey && p.KeyKind == tool.KeyIndividual {
				reason = "team_key_required"
			}
			out[i].Locked = append(out[i].Locked, lockedTool{Tool: t.Name(), Reason: reason})
		}
	}
	return out
}

// changedToolset is one toolset whose enabled state an enable/disable call
// flipped; an enabled one lists the tools it added and those still locked.
type changedToolset struct {
	Name    tool.Toolset `json:"name"`
	Enabled bool         `json:"enabled"`
	Tools   []string     `json:"tools,omitempty"`
	Locked  []lockedTool `json:"locked,omitempty"`
}

// changedSince reports only the toolsets whose enabled state differs from
// before; the client re-reads the full list via list_changed.
func (s *session) changedSince(before map[tool.Toolset]bool) []changedToolset {
	p := s.profile()
	out := []changedToolset{}
	for _, e := range s.listDetailed() {
		if before[e.Name] == e.Enabled {
			continue
		}
		c := changedToolset{Name: e.Name, Enabled: e.Enabled}
		if e.Enabled {
			c.Locked = e.Locked
			for _, t := range s.tools {
				if t.Meta().Toolset == e.Name && t.ListedIn(p) {
					c.Tools = append(c.Tools, t.Name())
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func (s *session) enableWritesAction(ctx context.Context, env *tool.Env) (*tool.Result, error) {
	if env.Profile.AllowWrites {
		return tool.Already(map[string]any{"writes": true}), nil
	}
	if s.deps.WritesLocked {
		return nil, errWritesLocked
	}
	ask, err := tool.AskHuman(ctx, env, fmt.Sprintf("Allow App Store Connect writes for profile %q for this session?", env.Profile.Name))
	if ask != nil {
		return ask, nil
	}
	if isElicitUnavailable(err) {
		return tool.OK(map[string]any{
			"writes":       false,
			"instructions": writesInstructions(clientName(ctx)),
		}), nil
	}
	if err != nil {
		return nil, fmt.Errorf("enable_writes: %w", err)
	}
	s.enableWrites()
	env.SetWrites(true)
	return tool.OK(map[string]any{"writes": true, "toolsets": s.listDetailed()}), nil
}

func clientName(ctx context.Context) string {
	ss := sessionFrom(ctx)
	if ss == nil {
		return ""
	}
	if p := ss.InitializeParams(); p != nil && p.ClientInfo != nil {
		return p.ClientInfo.Name
	}
	return ""
}

// writesInstructions returns copy-paste steps to restart app-store-connect-mcp with writes on
// for the detected MCP client.
func writesInstructions(client string) string {
	const tail = " Then restart the client. Alternatively set allow_writes = true in the profile in ~/.config/app-store-connect-mcp/config.toml."
	c := strings.ToLower(client)
	switch {
	case strings.Contains(c, "claude-code") || strings.Contains(c, "claude code"):
		return "Ask the user to run: claude mcp remove app-store-connect-mcp -s user && claude mcp add app-store-connect-mcp -s user -e ASC_MCP_ALLOW_WRITES=1 <existing -e ASC_* flags> -- app-store-connect-mcp." + tail
	case strings.Contains(c, "claude"):
		return `Ask the user to add "ASC_MCP_ALLOW_WRITES": "1" to mcpServers.app-store-connect-mcp.env in claude_desktop_config.json.` + tail
	case strings.Contains(c, "cursor"):
		return `Ask the user to add "ASC_MCP_ALLOW_WRITES": "1" to mcpServers.app-store-connect-mcp.env in ~/.cursor/mcp.json.` + tail
	case strings.Contains(c, "codex"):
		return `Ask the user to add ASC_MCP_ALLOW_WRITES = "1" to env of [mcp_servers.app-store-connect-mcp] in ~/.codex/config.toml.` + tail
	case strings.Contains(c, "visual studio code") || strings.Contains(c, "vscode"):
		return `Ask the user to add "ASC_MCP_ALLOW_WRITES": "1" to servers.app-store-connect-mcp.env in .vscode/mcp.json.` + tail
	case strings.Contains(c, "windsurf"):
		return `Ask the user to add "ASC_MCP_ALLOW_WRITES": "1" to mcpServers.app-store-connect-mcp.env in ~/.codeium/windsurf/mcp_config.json.` + tail
	default:
		return "Ask the user to set the environment variable ASC_MCP_ALLOW_WRITES=1 in this client's app-store-connect-mcp server config (or start app-store-connect-mcp with --allow-writes)." + tail
	}
}
