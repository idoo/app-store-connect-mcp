package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// errToolsetDisabled is the result of calling a tool whose toolset is off in
// this session.
var errToolsetDisabled = &tool.Error{
	Code:    "toolset_disabled",
	Message: "this tool's toolset is off in this session",
	Hint:    `call toolsets with action "enable" and this toolset; the tool appears after list_changed`,
}

// hiddenToolMiddleware answers a tools/call for a registered but unlisted
// tool with a tool error naming the unlock call, instead of the SDK's
// "unknown tool". Listed and truly unknown names pass through.
func (s *session) hiddenToolMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		r, ok := req.(*mcp.CallToolRequest)
		if !ok || r.Params == nil {
			return next(ctx, method, req)
		}
		if res := s.hiddenResult(r.Params.Name, r.Params.Arguments); res != nil {
			return s.render(res), nil
		}
		return next(ctx, method, req)
	}
}

// hiddenResult returns the error result for a hidden tool, or nil when name
// is listed, unknown or no longer hidden.
func (s *session) hiddenResult(name string, args json.RawMessage) *tool.Result {
	s.mu.Lock()
	listed := s.listed[name]
	s.mu.Unlock()
	if listed {
		return nil
	}
	i := slices.IndexFunc(s.tools, func(t tool.Tool) bool { return t.Name() == name })
	if i < 0 {
		return nil
	}
	if name == apiRequestName {
		if r := apiReadRedirect(args); r != nil {
			return r
		}
	}
	m := s.tools[i].Meta()
	p := s.profile()
	if m.NeedsTeamKey && p.KeyKind == tool.KeyIndividual {
		return tool.Fail(fmt.Errorf("%s: %w", name, tool.ErrIndividualKeyUnsupported))
	}
	writeLocked := s.writeLocked()[name]
	s.mu.Lock()
	on := s.on[m.Toolset]
	s.mu.Unlock()
	switch {
	case s.deps.Stateless && (!on || writeLocked):
		// Stateless HTTP fixes toolsets and write mode: no toolsets call can
		// help, only a restart with other flags.
		code := errToolsetDisabled.Code
		if on {
			code = tool.ErrWritesDisabled.Code
		}
		return tool.Fail(&tool.Error{Code: code, Message: name + ": " + errStatelessFixed.Message, Hint: errStatelessFixed.Hint})
	case !on:
		r := tool.Fail(errToolsetDisabled).Then(unlockToolset(m.Toolset))
		if writeLocked {
			r.Then(unlockWrites)
		}
		return r
	case writeLocked:
		return tool.Fail(fmt.Errorf("%s: %w", name, tool.ErrWritesDisabled))
	}
	return nil
}

// apiRequestName is the generic mutation tool; apiGetName is its read twin.
const apiRequestName, apiGetName = "asc_api_request", "asc_api_get"

// apiReadRedirect answers a read through the hidden asc_api_request (method
// empty or GET) with the asc_api_get call, so a read never asks to unlock
// writes. It is nil for mutations and unparsable arguments.
func apiReadRedirect(raw json.RawMessage) *tool.Result {
	var in struct {
		Method string         `json:"method"`
		Path   string         `json:"path"`
		Query  map[string]any `json:"query"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &in) != nil {
		return nil
	}
	if in.Method != "" && !strings.EqualFold(in.Method, "GET") {
		return nil
	}
	next := map[string]any{"path": in.Path}
	if len(in.Query) > 0 {
		next["query"] = in.Query
	}
	return tool.Fail(fmt.Errorf("%w: %s is for mutations; use %s for reads", tool.ErrInvalidArgs, apiRequestName, apiGetName)).
		Then(tool.NextStep{Tool: apiGetName, Args: next})
}
