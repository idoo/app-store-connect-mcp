package ascrules

import (
	"context"
	"time"
)

// ConsistencyRetries and ConsistencyWait bound the eventual-consistency 404
// retries of GETs flagged with [WithEventualConsistency].
const (
	ConsistencyRetries = 3
	ConsistencyWait    = time.Second
)

type consistencyKey struct{}

// WithEventualConsistency marks ctx so GETs answered 404 are retried
// (3 times, 1 s apart): use it to read an object right after creating it
// (new versions, buildUploads, review submissions), which Apple may not serve
// immediately.
func WithEventualConsistency(ctx context.Context) context.Context {
	return context.WithValue(ctx, consistencyKey{}, true)
}

// IsEventualConsistency reports whether ctx carries [WithEventualConsistency].
func IsEventualConsistency(ctx context.Context) bool {
	v, _ := ctx.Value(consistencyKey{}).(bool)
	return v
}
