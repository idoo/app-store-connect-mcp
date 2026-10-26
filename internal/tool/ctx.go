package tool

import (
	"context"
	"sync/atomic"
)

type ctxKey int

const (
	writeKey ctxKey = iota
	metaKey
)

// withWrite marks ctx as carrying the write capability. Only this package
// mints it: [Tool.Call] for unconfirmed Write tools, and the confirm gates
// ([RequireConfirm], [Preview], [Unconfirmed]) after they pass.
func withWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeKey, true)
}

// IsWrite reports whether ctx carries the write capability. The ASC client
// refuses any non-GET request whose context lacks it.
func IsWrite(ctx context.Context) bool {
	v, _ := ctx.Value(writeKey).(bool)
	return v
}

// call is the per-call state the confirm gates read and update.
type call struct {
	meta Meta
	// approvedBy is "human", "agent" or "" (none), set by the confirm gates.
	approvedBy string
	// args are the decoded tool arguments; [RequireConfirm] binds previews
	// and confirms to them.
	args map[string]any
	// appleRequests counts HTTP attempts sent to Apple during the call.
	appleRequests atomic.Int64
}

// CountAppleRequest records one HTTP attempt (retries and pages included)
// against the tool call carried by ctx; outside a call it does nothing. The
// count is reported as _meta.app-store-connect-mcp.requests.apple.
func CountAppleRequest(ctx context.Context) {
	if c := callFrom(ctx); c != nil {
		c.appleRequests.Add(1)
	}
}

func withCall(ctx context.Context, c *call) context.Context {
	return context.WithValue(ctx, metaKey, c)
}

func callFrom(ctx context.Context) *call {
	c, _ := ctx.Value(metaKey).(*call)
	return c
}
