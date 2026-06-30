// Command goforge is the GoForge CLI entry point. With no subcommand it parses
// configuration and hands off to the interactive app in internal/cli (the
// historical behavior). The serve/run/status/resume subcommands expose the M7
// control plane. All behavior lives in internal/cli so it can be tested and
// reused by other edge entry points.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Misakaworstlv999/goforge/internal/cli"
	"github.com/Misakaworstlv999/goforge/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches on the first argument. A recognized subcommand (a bare word, not
// a flag) routes to its handler; anything else — including flags like
// "-mode agent" or no args at all — falls through to the interactive REPL, so
// existing invocations behave exactly as before.
func run(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "serve":
			return cli.Serve(args[1:])
		case "run":
			return cli.RunCmd(args[1:])
		case "status":
			return cli.Status(args[1:])
		case "resume":
			return cli.Resume(args[1:])
		case "help":
			usage(os.Stdout)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
			usage(os.Stderr)
			return 2
		}
	}
	return repl(args)
}

// repl runs the historical interactive loop (no subcommand).
func repl(args []string) int {
	cfg, err := config.Parse(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := cli.New(cfg, os.Stdout).Run(context.Background(), os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func usage(w *os.File) {
	fmt.Fprint(w, `goforge — AI development workflow engine

Usage:
  goforge [flags]                      Start the interactive REPL (default)
  goforge serve [flags]                Start the HTTP control plane
  goforge run [flags] <task>           Trigger one run and stream its events
  goforge status -store <p> <run-id>   Inspect a persisted run
  goforge resume -store <p> <run-id>   Resume a paused run
  goforge help                         Show this help

Run "goforge -help" for the full flag list.
`)
}
