// Command goforge is the GoForge CLI entry point. It parses configuration and
// hands off to the interactive app in internal/cli; all behavior lives there so
// it can be tested and reused by future edge entry points (HTTP, MCP).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Misakaworstlv999/goforge/internal/cli"
	"github.com/Misakaworstlv999/goforge/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := cli.New(cfg, os.Stdout).Run(context.Background(), os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
