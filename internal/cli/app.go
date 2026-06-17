// Package cli is the Ring 5 interactive entry point. It wires configuration into
// an LLM client and an agent, and drives a read-eval-print loop. It holds only
// concrete orchestration types — the abstractions it consumes (llm.LLM,
// agent.Agent, tool.Registry) live in the inner rings.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
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
	t, bannerText := a.turnForMode(ctx)
	return repl(in, a.out, bannerText, t)
}

// turnForMode builds the per-line handler and banner for the configured mode,
// setting up any state (registry, agent) the mode needs exactly once.
func (a *App) turnForMode(ctx context.Context) (turn, string) {
	switch a.cfg.Mode {
	case config.ModeTools:
		reg := a.buildRegistry()
		return toolsTurn(ctx, a.client, reg, a.out, a.cfg.System), banner(a.cfg.Mode, a.cfg.MaxSteps, reg)
	case config.ModeAgent:
		reg := a.buildRegistry()
		agt := agent.New(a.client, reg,
			agent.WithSystemPrompt(a.cfg.System),
			agent.WithMaxSteps(a.cfg.MaxSteps),
			agent.WithToolTimeout(a.cfg.ToolTimeout),
		)
		return agentTurn(ctx, agt, a.out), banner(a.cfg.Mode, a.cfg.MaxSteps, reg)
	default:
		return chatTurn(ctx, a.client, a.out, a.cfg.System), banner(a.cfg.Mode, a.cfg.MaxSteps, nil)
	}
}

// buildRegistry assembles the toolset: calculator + clock always, file tools
// (read/write/list) sandboxed to the configured workdirs, and exec_command only
// when a command allowlist is configured (arbitrary exec is opt-in). If the
// sandbox cannot be created the file/shell tools are skipped with a warning.
func (a *App) buildRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	_ = reg.Register(builtin.NewCalculator(), builtin.NewClock())

	roots := a.cfg.Workdirs
	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil {
			roots = []string{wd}
		}
	}

	sb, err := builtin.NewSandbox(roots,
		builtin.WithAllowedCommands(a.cfg.AllowCommands...),
		builtin.WithCommandTimeout(a.cfg.ToolTimeout),
	)
	if err != nil {
		fmt.Fprintf(a.out, "warning: file/shell tools disabled: %v\n", err)
		return reg
	}

	_ = reg.Register(builtin.NewReadFile(sb), builtin.NewWriteFile(sb), builtin.NewListFiles(sb))
	if len(a.cfg.AllowCommands) > 0 {
		_ = reg.Register(builtin.NewExecCommand(sb))
	}
	return reg
}
