package mcpserver

import (
	"context"
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
