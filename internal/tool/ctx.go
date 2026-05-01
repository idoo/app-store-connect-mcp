package tool

import "context"

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

func withMeta(ctx context.Context, m *Meta) context.Context {
	return context.WithValue(ctx, metaKey, m)
}

func metaFrom(ctx context.Context) *Meta {
	m, _ := ctx.Value(metaKey).(*Meta)
	return m
}

// Name returns the name of the tool executing in ctx, or "".
func Name(ctx context.Context) string {
	if m := metaFrom(ctx); m != nil {
		return m.Name
	}
	return ""
}
