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
	"sort"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
	"github.com/Misakaworstlv999/goforge/pkg/tool/mcpclient"
)

// App is the composed CLI application: a configuration, an LLM client built from
// it, and an output sink. Construct it with New and drive it with Run.
type App struct {
	cfg     config.Config
	client  llm.LLM
	out     io.Writer
	closers []io.Closer // MCP clients to tear down on exit
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
	defer a.closeAll()
	return repl(in, a.out, bannerText, t)
}

// closeAll tears down any connected MCP clients.
func (a *App) closeAll() {
	for _, c := range a.closers {
		_ = c.Close()
	}
}

// turnForMode builds the per-line handler and banner for the configured mode,
// setting up any state (registry, agent) the mode needs exactly once.
func (a *App) turnForMode(ctx context.Context) (turn, string) {
	switch a.cfg.Mode {
	case config.ModeTools:
		reg := a.buildRegistry(ctx)
		return toolsTurn(ctx, a.client, reg, a.out, a.cfg.System), banner(a.cfg.Mode, a.cfg.MaxSteps, reg)
	case config.ModeAgent:
		reg := a.buildRegistry(ctx)
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

// buildRegistry assembles the App's toolset and tracks any MCP clients for
// teardown. It delegates to the reusable BuildRegistry so other edge entry
// points (serve/run) compose the identical toolset.
func (a *App) buildRegistry(ctx context.Context) *tool.Registry {
	reg, closers := BuildRegistry(ctx, a.cfg, a.out)
	a.closers = append(a.closers, closers...)
	return reg
}

// BuildRegistry assembles the toolset from config: calculator + clock always,
// file tools (read/write/list) sandboxed to the configured workdirs, and
// exec_command only when a command allowlist is configured (arbitrary exec is
// opt-in). If the sandbox cannot be created the file/shell tools are skipped
// with a warning. MCP servers from cfg.MCPConfigPath are registered (direct or
// broker per cfg.MCPExpose). It returns the registry and any io.Closers (MCP
// clients) the caller must Close on shutdown. It is the single composition point
// for the toolset, reused by the interactive REPL and the serve/run subcommands.
func BuildRegistry(ctx context.Context, cfg config.Config, out io.Writer) (*tool.Registry, []io.Closer) {
	reg := tool.NewRegistry()
	_ = reg.Register(builtin.NewCalculator(), builtin.NewClock())

	roots := cfg.Workdirs
	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil {
			roots = []string{wd}
		}
	}

	sb, err := builtin.NewSandbox(roots,
		builtin.WithAllowedCommands(cfg.AllowCommands...),
		builtin.WithCommandTimeout(cfg.ToolTimeout),
	)
	if err != nil {
		fmt.Fprintf(out, "warning: file/shell tools disabled: %v\n", err)
	} else {
		_ = reg.Register(builtin.NewReadFile(sb), builtin.NewWriteFile(sb), builtin.NewListFiles(sb))
		if len(cfg.AllowCommands) > 0 {
			_ = reg.Register(builtin.NewExecCommand(sb))
		}
	}

	closers := registerMCPServers(ctx, cfg, reg, out)
	return reg, closers
}

// registerMCPServers loads the standard mcpServers config and registers each
// server's tools, returning the connected clients as io.Closers. A missing
// config file means no servers; a server that fails to parse or connect is
// skipped with a warning so it can't take down the rest of the toolset. Servers
// are processed in sorted name order for determinism.
func registerMCPServers(ctx context.Context, cfg config.Config, reg *tool.Registry, out io.Writer) []io.Closer {
	sc, err := mcpclient.LoadServersConfig(cfg.MCPConfigPath)
	if err != nil {
		fmt.Fprintf(out, "warning: MCP config %s ignored: %v\n", cfg.MCPConfigPath, err)
		return nil
	}

	names := make([]string, 0, len(sc.MCPServers))
	for name := range sc.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var mcpTools []tool.Tool
	var closers []io.Closer
	for _, name := range names {
		mcfg, err := sc.MCPServers[name].ToConfig(name)
		if err != nil {
			fmt.Fprintf(out, "warning: MCP server %q skipped: %v\n", name, err)
			continue
		}
		mcfg.ToolPrefix = name // namespace this server's tools (avoid collisions)
		client, err := mcpclient.New(ctx, mcfg)
		if err != nil {
			fmt.Fprintf(out, "warning: MCP server %q disabled: %v\n", name, err)
			continue
		}
		tools, err := client.Tools(ctx)
		if err != nil {
			fmt.Fprintf(out, "warning: MCP server %q tools skipped: %v\n", name, err)
			_ = client.Close()
			continue
		}
		mcpTools = append(mcpTools, tools...)
		closers = append(closers, client)
	}
	if len(mcpTools) == 0 {
		return closers
	}

	// direct: register every tool; broker: expose 3 meta-tools (progressive
	// disclosure) instead, keeping the system prompt small.
	if cfg.MCPExpose == config.MCPExposeBroker {
		if err := reg.RegisterSet(ctx, mcpclient.NewBroker(mcpTools)); err != nil {
			fmt.Fprintf(out, "warning: MCP broker tools skipped: %v\n", err)
		}
		return closers
	}
	if err := reg.Register(mcpTools...); err != nil {
		fmt.Fprintf(out, "warning: some MCP tools skipped: %v\n", err)
	}
	return closers
}
