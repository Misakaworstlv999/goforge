// Package config holds the cross-cutting runtime configuration for GoForge's
// edge layer (Ring 5). It parses CLI flags and environment into a plain Config
// struct that any entry point — CLI today, HTTP/MCP in M7 — can consume.
package config

import (
	"flag"
	"fmt"
	"io"
	"time"
)

// Mode selects which interactive behavior the CLI runs. The three modes map to
// the project's milestones: chat (M1 streaming), tools (M2 single-step tool
// calling), and agent (M3 ReAct loop).
type Mode string

const (
	ModeChat  Mode = "chat"
	ModeTools Mode = "tools"
	ModeAgent Mode = "agent"
)

// valid reports whether m is a recognized mode.
func (m Mode) valid() bool {
	switch m {
	case ModeChat, ModeTools, ModeAgent:
		return true
	default:
		return false
	}
}

// Config is the resolved runtime configuration. It is provider-agnostic data;
// turning it into an llm.LLM is the composition layer's job (see internal/cli).
type Config struct {
	Provider    string
	Model       string
	BaseURL     string
	APIKey      string
	System      string
	Mode        Mode
	MaxSteps    int
	ToolTimeout time.Duration
}

// Default values shared by Parse and tests.
const (
	defaultSystem      = "You are a helpful assistant. You can use tools when needed."
	defaultMaxSteps    = 10
	defaultToolTimeout = 30 * time.Second
)

// Parse builds a Config from CLI args and an environment lookup function. It
// uses a dedicated FlagSet (not the global flag package) so it is fully testable
// and never touches os.Args directly. getenv is injected for the same reason;
// pass os.Getenv from main.
func Parse(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("goforge", flag.ContinueOnError)
	// Suppress the FlagSet's own usage dump on error; callers report the
	// returned error themselves (see cmd/goforge/main.go).
	fs.SetOutput(io.Discard)

	var cfg Config
	var mode string
	fs.StringVar(&cfg.Provider, "provider", "openai", "LLM provider: openai or anthropic")
	fs.StringVar(&cfg.Model, "model", "", "Model name (e.g., gpt-4o, claude-sonnet-4-20250514)")
	fs.StringVar(&cfg.BaseURL, "base-url", "", "Custom API base URL")
	fs.StringVar(&cfg.APIKey, "api-key", "", "API key (defaults to OPENAI_API_KEY or ANTHROPIC_API_KEY)")
	fs.StringVar(&cfg.System, "system", defaultSystem, "System prompt")
	fs.StringVar(&mode, "mode", string(ModeAgent), "Interactive mode: chat | tools | agent")
	fs.IntVar(&cfg.MaxSteps, "max-steps", defaultMaxSteps, "Max ReAct steps (agent mode only)")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", defaultToolTimeout, "Per-tool execution timeout (agent mode only)")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.Mode = Mode(mode)
	if !cfg.Mode.valid() {
		return Config{}, fmt.Errorf("invalid -mode %q: want chat, tools, or agent", mode)
	}

	if cfg.APIKey == "" {
		cfg.APIKey = getenv(apiKeyEnv(cfg.Provider))
	}

	return cfg, nil
}

// apiKeyEnv returns the environment variable name that holds the API key for the
// given provider.
func apiKeyEnv(provider string) string {
	switch provider {
	case "anthropic", "claude":
		return "ANTHROPIC_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}
