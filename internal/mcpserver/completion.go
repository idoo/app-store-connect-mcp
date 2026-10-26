package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Completion limits.
const (
	completionMax     = 100 // the protocol's cap on returned values
	completionAppsTTL = 5 * time.Minute
)

// completionPlatforms are the App Store Connect platform values.
var completionPlatforms = []string{"IOS", "MAC_OS", "TV_OS", "VISION_OS"}

// appsCache holds the session's app names and bundle ids for completion: at
// most one /v1/apps GET per session per completionAppsTTL.
type appsCache struct {
	mu     sync.Mutex
	at     time.Time
	values []string
}

// complete answers completion/complete for prompt and resource-template
// arguments. It never mutates and only reads Apple through the apps cache.
func (s *session) complete(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	p := req.Params
	if p == nil || p.Ref == nil {
		return nil, fmt.Errorf("%w: completion needs a ref", tool.ErrInvalidArgs)
	}
	prefix := strings.ToLower(p.Argument.Value)
	var candidates []string
	switch p.Argument.Name {
	case "app":
		apps, err := s.completionApps(ctx)
		if err != nil {
			return nil, fmt.Errorf("complete app: %w", err)
		}
		candidates = apps
	case "platform":
		candidates = completionPlatforms
	case "operationId":
		if s.deps.Env.Spec != nil && p.Argument.Value != "" {
			for _, h := range s.deps.Env.Spec.Search(p.Argument.Value, completionMax) {
				candidates = append(candidates, h.ID)
			}
			prefix = "" // search ranks by keyword, not prefix
		}
	}
	vals := make([]string, 0, min(len(candidates), completionMax))
	total := 0
	for _, c := range candidates {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(c), prefix) {
			continue
		}
		total++
		if len(vals) < completionMax {
			vals = append(vals, c)
		}
	}
	return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{
		Values: vals, Total: total, HasMore: total > len(vals),
	}}, nil
}

// completionApps returns cached app names and bundle ids, refreshing them at
// most once per completionAppsTTL.
func (s *session) completionApps(ctx context.Context) ([]string, error) {
	c := &s.appsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if !c.at.IsZero() && now.Sub(c.at) < completionAppsTTL {
		return c.values, nil
	}
	if s.deps.Env.ASC == nil {
		return nil, fmt.Errorf("apps: %w", tool.ErrUnavailable)
	}
	res, err := s.deps.Env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields:   map[string][]string{"apps": {"name", "bundleId"}},
		MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	var vals []string
	for _, r := range res.Data {
		for _, k := range []string{"name", "bundleId"} {
			if v, ok := r.Attributes[k].(string); ok && v != "" && !slices.Contains(vals, v) {
				vals = append(vals, v)
			}
		}
	}
	c.at, c.values = now, vals
	return vals, nil
}

// now is the session clock (Deps.Now or time.Now).
func (s *session) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}
