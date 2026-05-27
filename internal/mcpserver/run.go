package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunStdio serves srv over stdin/stdout until ctx ends or the client
// disconnects. It keeps the real stdout for the protocol and points os.Stdout
// at stderr for the duration, so stray writes cannot corrupt the JSON-RPC
// stream.
func RunStdio(ctx context.Context, srv *mcp.Server) error {
	out := os.Stdout                   //nolint:forbidigo // the protocol owns the real stdout.
	os.Stdout = os.Stderr              //nolint:forbidigo // stray prints go to stderr.
	defer func() { os.Stdout = out }() //nolint:forbidigo // restore on exit.
	if err := srv.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: out}); err != nil {
		return fmt.Errorf("mcpserver: stdio: %w", err)
	}
	return nil
}

// ListBytes returns the size of the JSON encoding of every tool srv lists,
// i.e. the context cost of tools/list.
func ListBytes(ctx context.Context, srv *mcp.Server) (int, error) {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return 0, fmt.Errorf("mcpserver: connect: %w", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "hamn-budget", Version: "0"}, nil).Connect(ctx, ct, nil)
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
