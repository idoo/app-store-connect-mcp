package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/tool"
)

type sessionKey struct{}

// sessionFrom returns the MCP session serving the current request, if any.
func sessionFrom(ctx context.Context) *mcp.ServerSession {
	ss, _ := ctx.Value(sessionKey{}).(*mcp.ServerSession)
	return ss
}

// handler adapts a tool to the SDK. Tool failures, including panics, become
// isError results, never JSON-RPC errors.
func (s *session) handler(t tool.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		defer func() {
			if p := recover(); p != nil {
				res = s.render(tool.Fail(fmt.Errorf("%s: %w: panic: %v", t.Name(), tool.ErrInternal, p)))
				err = nil
			}
		}()
		ctx = context.WithValue(ctx, sessionKey{}, req.Session)
		var args json.RawMessage
		if req.Params != nil {
			args = req.Params.Arguments
		}
		return s.render(t.Call(ctx, s.env(ctx, req), args)), nil
	}
}

// env builds the per-request environment from the base Env.
func (s *session) env(ctx context.Context, req *mcp.CallToolRequest) *tool.Env {
	e := s.deps.Env
	e.Profile = s.profile()
	e.Plans = s.plans
	e.Toolsets = s
	e.Version = s.deps.Version
	e.Progress = nil
	e.Elicit = elicitFunc(req.Session)
	if req.Params != nil && req.Session != nil {
		if token := req.Params.GetProgressToken(); token != nil {
			ss := req.Session
			e.Progress = func(done, total float64, msg string) {
				// Progress is best-effort; a failed notification must not fail the tool.
				_ = ss.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token, Progress: done, Total: total, Message: msg,
				})
			}
		}
	}
	return &e
}

var approveSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"approve": map[string]any{"type": "boolean", "title": "Approve", "default": false},
	},
	"required": []string{"approve"},
}

// elicitFunc returns a yes/no elicitation for ss, or nil when the client did
// not declare elicitation (tool.Call then reports ErrElicitationUnavailable).
func elicitFunc(ss *mcp.ServerSession) func(context.Context, string) (bool, error) {
	if !canElicit(ss) {
		return nil
	}
	return func(ctx context.Context, msg string) (bool, error) {
		r, err := ss.Elicit(ctx, &mcp.ElicitParams{Message: asc.Redact(msg), RequestedSchema: approveSchema})
		if err != nil {
			return false, fmt.Errorf("elicit approval: %w", err)
		}
		approved, _ := r.Content["approve"].(bool)
		return r.Action == "accept" && approved, nil
	}
}

// mrtrProtocol is the first protocol version (SEP-2322) on which servers may
// not send elicitation/create while serving a request; such clients must be
// asked through multi round-trip input requests instead.
const mrtrProtocol = "2026-07-28"

// canElicit reports whether ss declared elicitation on a protocol version
// that allows in-request elicitation. On newer versions hamn fails closed
// (Elicit reports ErrElicitationUnavailable).
func canElicit(ss *mcp.ServerSession) bool {
	if ss == nil {
		return false
	}
	p := ss.InitializeParams()
	return p != nil && p.Capabilities != nil && p.Capabilities.Elicitation != nil && p.ProtocolVersion < mrtrProtocol
}

// render converts a tool result into an MCP result: structuredContent is
// {data, next, _meta:{hamn}}, the text content is its compact JSON (or
// Result.Text), images follow, and everything is capped and redacted.
func (s *session) render(r *tool.Result) *mcp.CallToolResult {
	s.markLocked(r)
	data, trunc, err := asc.CapResult(r.Data, s.deps.MaxResultBytes)
	if err != nil {
		r = tool.Fail(fmt.Errorf("%w: render result: %v", tool.ErrInternal, err))
		data = r.Data
	}
	if trunc != nil {
		r.Meta.Truncated = trunc
	}
	obj := map[string]any{"_meta": map[string]any{"hamn": r.Meta}}
	if data != nil {
		obj["data"] = data
	}
	if len(r.Next) > 0 {
		obj["next"] = r.Next
	}
	b, err := json.Marshal(obj)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"data": tool.BodyFor(fmt.Errorf("%w: encode result: %v", tool.ErrInternal, err))})
		r.IsError = true
	}
	b = asc.RedactBytes(b)
	text := string(b)
	if r.Text != "" {
		text = asc.Redact(r.Text)
	}
	out := &mcp.CallToolResult{
		IsError:           r.IsError,
		StructuredContent: json.RawMessage(b),
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
	}
	for _, img := range r.Images {
		out.Content = append(out.Content, &mcp.ImageContent{MIMEType: img.MIMEType, Data: img.Data})
	}
	return out
}
