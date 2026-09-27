// Command hamn is the App Store Connect MCP server.
//
// With no arguments it serves MCP over stdio. Subcommands manage credentials
// profiles, diagnose the setup and configure MCP clients; they never run the
// server and print to stdout normally.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `hamn — App Store Connect MCP server

Usage:
  hamn [serve] [flags]          serve MCP over stdio (or --http ADDR)
  hamn auth <add|import|list|test|remove|set>
  hamn doctor [--profile NAME]  check config, key, Apple reachability
  hamn config snippet <client>  print client config (claude-code, claude-desktop, codex, cursor, vscode, windsurf)
  hamn config install <client>  write it into the client's config (with backup)
  hamn skills <list|install>    install agent skills (--client claude-code|codex|cursor|omp, --dir, --force)
  hamn version
`

// usageError is a command-line misuse; it exits with status 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErr(msg string) error { return &usageError{msg: msg} }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr) //nolint:forbidigo // CLI output.
	stop()
	var ue *usageError
	switch {
	case err == nil:
	case errors.As(err, &ue):
		fmt.Fprintln(os.Stderr, "hamn:", err)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "hamn:", err)
		os.Exit(1)
	}
}

// run dispatches a subcommand. Only serve writes the MCP protocol to stdout.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "" && args[0][0] == '-') {
		return runServe(ctx, args, stderr)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return runServe(ctx, rest, stderr)
	case "auth":
		return runAuth(ctx, rest, stdin, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, rest, stdout, stderr)
	case "config":
		return runConfig(rest, stdout, stderr)
	case "skills":
		return runSkills(rest, stdout, stderr)
	case "version", "--version":
		_, err := fmt.Fprintln(stdout, "hamn", buildVersion())
		return err
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage)
		return err
	default:
		return usageErr(fmt.Sprintf("unknown command %q\n\n%s", cmd, usage))
	}
}
