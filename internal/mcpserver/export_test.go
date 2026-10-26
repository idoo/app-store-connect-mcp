package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ListBytes returns the size of the JSON encoding of every tool srv lists,
// i.e. the context cost of tools/list. Test-only: the budget tests use it.
func ListBytes(ctx context.Context, srv *mcp.Server) (int, error) {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return 0, fmt.Errorf("mcpserver: connect: %w", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "app-store-connect-mcp-budget", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return 0, fmt.Errorf("mcpserver: client connect: %w", err)
	}
	defer func() { _ = cs.Close() }()
	var tools []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return 0, fmt.Errorf("mcpserver: list tools: %w", err)
		}
		tools = append(tools, t)
	}
	b, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return 0, fmt.Errorf("mcpserver: encode tools: %w", err)
	}
	return len(b), nil
}
