// Command app-store-connect-mcp is the App Store Connect MCP server.
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

const usage = `app-store-connect-mcp — App Store Connect MCP server

Usage:
  app-store-connect-mcp [serve] [flags]          serve MCP over stdio (or --http ADDR); app-store-connect-mcp serve --help lists flags
  app-store-connect-mcp auth <add|import|list|test|remove|set>
  app-store-connect-mcp doctor [--profile NAME] [--json]  check config, key, Apple reachability
  app-store-connect-mcp guard --app X [--platform P] [--pause-on-breach] [thresholds]  release health for cron/CI (exit 0/3/4/5)
  app-store-connect-mcp aso <track|report> --app X [--keywords a,b] [--storefronts us,se] [--depth N]  keyword ranks for cron
  app-store-connect-mcp config snippet <client>  print client config (claude-code, claude-desktop, codex, cursor, vscode, windsurf)
  app-store-connect-mcp config install <client>  write it into the client's config (with backup)
  app-store-connect-mcp skills <list|install>    install agent skills (--client claude-code|codex|cursor|omp, --dir, --force)
  app-store-connect-mcp web <login|import|status|team|logout>  Apple ID web session for the opt-in "web" toolset
  app-store-connect-mcp version

Any subcommand with --help prints its usage and does nothing else.
`

// subUsage is the usage text printed for `app-store-connect-mcp <cmd> --help`.
var subUsage = map[string]string{
	"auth":    authUsage,
	"config":  configUsage,
	"skills":  skillsUsage,
	"guard":   guardUsage,
	"aso":     asoUsage,
	"doctor":  "usage: app-store-connect-mcp doctor [--profile NAME] [--json]\n  checks the config, key, JWT, Apple reachability, clock skew and rate limit",
	"web":     webUsage,
	"version": "usage: app-store-connect-mcp version",
}

// usageError is a command-line misuse; it exits with status 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErr(msg string) error { return &usageError{msg: msg} }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr) //nolint:forbidigo // CLI output.
	stop()
	var ue *usageError
	var ge *guardExit
	switch {
	case err == nil:
	case errors.As(err, &ge):
		os.Exit(ge.code)
	case errors.As(err, &ue):
		fmt.Fprintln(os.Stderr, "app-store-connect-mcp:", err)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "app-store-connect-mcp:", err)
		os.Exit(1)
	}
}

// run dispatches a subcommand. Only serve writes the MCP protocol to stdout.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		if _, err := io.WriteString(stdout, usage+"\nserve flags:\n"); err != nil {
			return err
		}
		return runServe(ctx, []string{"-h"}, stdout)
	}
	if len(args) == 0 || (args[0] != "" && args[0][0] == '-') {
		return runServe(ctx, args, stderr)
	}
	cmd, rest := args[0], args[1:]
	if u, ok := subUsage[cmd]; ok && wantsHelp(rest) {
		_, err := fmt.Fprintln(stdout, u)
		return err
	}
	switch cmd {
	case "serve":
		if wantsHelp(rest) {
			return runServe(ctx, []string{"-h"}, stdout)
		}
		return runServe(ctx, rest, stderr)
	case "auth":
		return runAuth(ctx, rest, stdin, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, rest, stdout, stderr)
	case "guard":
		return runGuard(ctx, rest, stdout, stderr)
	case "aso":
		return runAso(ctx, rest, stdout, stderr)
	case "config":
		return runConfig(rest, stdout, stderr)
	case "skills":
		return runSkills(rest, stdout, stderr)
	case "web":
		return runWeb(ctx, rest, stdin, stdout, stderr)
	case "version", "--version":
		_, err := fmt.Fprintln(stdout, "app-store-connect-mcp", buildVersion())
		return err
	default:
		return usageErr(fmt.Sprintf("unknown command %q\n\n%s", cmd, usage))
	}
}
