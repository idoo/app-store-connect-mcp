package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/idoo/hamn/internal/tool"
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

// toolsetsTool builds the session's toolsets tool (core, read, local).
func (s *session) toolsetsTool() tool.Tool {
	names := make([]any, len(tool.AllToolsets))
	for i, ts := range tool.AllToolsets {
		names[i] = string(ts)
	}
	return tool.New(tool.Meta{
		Name:        "toolsets",
		Toolset:     tool.Core,
		Title:       "Toolsets",
		Description: "List toolsets, enable or disable them for this session (tool list refreshes), or ask the user to allow App Store Connect writes for this session.",
		Class:       tool.Read,
		Local:       true,
		Enums: map[string][]any{
			"action":   {"list", "enable", "disable", "enable_writes"},
			"toolsets": names,
		},
	}, s.toolsetsHandler)
}

func (s *session) toolsetsHandler(ctx context.Context, env *tool.Env, in toolsetsIn) (*tool.Result, error) {
	switch in.Action {
	case "list":
		return tool.OK(map[string]any{"writes": env.Profile.AllowWrites, "toolsets": s.listDetailed()}), nil
	case "enable", "disable":
		ts := make([]tool.Toolset, len(in.Toolsets))
		for i, n := range in.Toolsets {
			ts[i] = tool.Toolset(n)
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
		return tool.OK(map[string]any{"toolsets": s.listDetailed()}), nil
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

func (s *session) enableWritesAction(ctx context.Context, env *tool.Env) (*tool.Result, error) {
	if env.Profile.AllowWrites {
		return tool.Already(map[string]any{"writes": true}), nil
	}
	if s.deps.WritesLocked {
		return nil, errWritesLocked
	}
	ok, err := env.Elicit(ctx, fmt.Sprintf("Allow App Store Connect writes for profile %q for this session?", env.Profile.Name))
	if isElicitUnavailable(err) {
		return tool.OK(map[string]any{
			"writes":       false,
			"instructions": writesInstructions(clientName(ctx)),
		}), nil
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("enable_writes: %w", tool.ErrDeclined)
	}
	s.enableWrites()
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

// writesInstructions returns copy-paste steps to restart hamn with writes on
// for the detected MCP client.
func writesInstructions(client string) string {
	const tail = " Then restart the client. Alternatively set allow_writes = true in the profile in ~/.config/hamn/config.toml."
	c := strings.ToLower(client)
	switch {
	case strings.Contains(c, "claude-code") || strings.Contains(c, "claude code"):
		return "Ask the user to run: claude mcp remove hamn -s user && claude mcp add hamn -s user -e HAMN_ALLOW_WRITES=1 <existing -e ASC_* flags> -- hamn." + tail
	case strings.Contains(c, "claude"):
		return `Ask the user to add "HAMN_ALLOW_WRITES": "1" to mcpServers.hamn.env in claude_desktop_config.json.` + tail
	case strings.Contains(c, "cursor"):
		return `Ask the user to add "HAMN_ALLOW_WRITES": "1" to mcpServers.hamn.env in ~/.cursor/mcp.json.` + tail
	case strings.Contains(c, "codex"):
		return `Ask the user to add HAMN_ALLOW_WRITES = "1" to env of [mcp_servers.hamn] in ~/.codex/config.toml.` + tail
	case strings.Contains(c, "visual studio code") || strings.Contains(c, "vscode"):
		return `Ask the user to add "HAMN_ALLOW_WRITES": "1" to servers.hamn.env in .vscode/mcp.json.` + tail
	case strings.Contains(c, "windsurf"):
		return `Ask the user to add "HAMN_ALLOW_WRITES": "1" to mcpServers.hamn.env in ~/.codeium/windsurf/mcp_config.json.` + tail
	default:
		return "Ask the user to set the environment variable HAMN_ALLOW_WRITES=1 in this client's hamn server config (or start hamn with --allow-writes)." + tail
	}
}
