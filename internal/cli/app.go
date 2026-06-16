// Package cli is the Ring 5 interactive entry point. It wires configuration into
// an LLM client and an agent, and drives a read-eval-print loop. It holds only
// concrete orchestration types — the abstractions it consumes (llm.LLM,
// agent.Agent, tool.Registry) live in the inner rings.
package cli

import (
	"context"
	"io"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// App is the composed CLI application: a configuration, an LLM client built from
// it, and an output sink. Construct it with New and drive it with Run.
type App struct {
	cfg    config.Config
	client llm.LLM
	out    io.Writer
}

// New builds an App, constructing the LLM client from cfg via NewLLM.
func New(cfg config.Config, out io.Writer) *App {
	return &App{cfg: cfg, client: NewLLM(cfg), out: out}
}

// newWithClient injects a client directly; used by tests with a mock LLM.
func newWithClient(cfg config.Config, client llm.LLM, out io.Writer) *App {
	return &App{cfg: cfg, client: client, out: out}
}

// Run selects the turn handler for the configured mode and drives the shared
// REPL reading from in until EOF or "exit".
func (a *App) Run(ctx context.Context, in io.Reader) error {
	t := a.turnForMode(ctx)
	return repl(in, a.out, banner(a.cfg.Mode, a.cfg.MaxSteps), t)
}

// turnForMode builds the per-line handler for the configured mode, setting up
// any state (registry, agent) the mode needs exactly once.
func (a *App) turnForMode(ctx context.Context) turn {
	switch a.cfg.Mode {
	case config.ModeTools:
		return toolsTurn(ctx, a.client, newToolRegistry(), a.out, a.cfg.System)
	case config.ModeAgent:
		agt := agent.New(a.client, newToolRegistry(),
			agent.WithSystemPrompt(a.cfg.System),
			agent.WithMaxSteps(a.cfg.MaxSteps),
			agent.WithToolTimeout(a.cfg.ToolTimeout),
		)
		return agentTurn(ctx, agt, a.out)
	default:
		return chatTurn(ctx, a.client, a.out, a.cfg.System)
	}
}
