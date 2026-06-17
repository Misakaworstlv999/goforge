// Package config holds the cross-cutting runtime configuration for GoForge's
// edge layer (Ring 5). It resolves settings from four layers, lowest precedence
// first: struct defaults < .env file < process environment < CLI flags. The
// result is a plain Config struct that any entry point — CLI today, HTTP/MCP in
// M7 — can consume.
package config

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
	// Workdirs are the sandbox root directories for file/shell tools (comma-
	// separated in env/flags). Empty means only the current working directory,
	// resolved by the composition layer.
	Workdirs []string
	// AllowCommands is the shell command allowlist. Empty disables exec_command.
	AllowCommands []string
	// MCPServers lists external MCP servers to connect (stdio). Each entry is a
	// command line, e.g. "npx -y @modelcontextprotocol/server-filesystem /path".
	// Their tools are discovered and registered alongside the builtins.
	MCPServers []string
}

// Default values shared by Parse and tests.
const (
	defaultSystem      = "You are a helpful assistant. You can use tools when needed."
	defaultMaxSteps    = 10
	defaultToolTimeout = 30 * time.Second
	defaultEnvFile     = ".env"
)

// Environment variable names. Non-secret app settings use a GOFORGE_ prefix;
// API keys keep their conventional provider-specific names.
const (
	envProvider      = "GOFORGE_PROVIDER"
	envModel         = "GOFORGE_MODEL"
	envBaseURL       = "GOFORGE_BASE_URL"
	envSystem        = "GOFORGE_SYSTEM"
	envMode          = "GOFORGE_MODE"
	envMaxSteps      = "GOFORGE_MAX_STEPS"
	envToolTimeout   = "GOFORGE_TOOL_TIMEOUT"
	envWorkdir       = "GOFORGE_WORKDIR"
	envAllowCommands = "GOFORGE_ALLOW_COMMANDS"
	envMCPServers    = "GOFORGE_MCP_SERVERS"
)

// Parse builds a Config from CLI args and an environment lookup function, also
// consulting a .env file in the current directory if present. getenv is injected
// (pass os.Getenv from main) so the function stays testable and free of global
// state.
func Parse(args []string, getenv func(string) string) (Config, error) {
	return ParseWithEnvFile(args, getenv, defaultEnvFile)
}

// ParseWithEnvFile is Parse with an explicit .env path, used by tests. A missing
// file is not an error. Precedence (low→high): defaults < .env < process env <
// flags. Process environment wins over .env (principle of least surprise).
func ParseWithEnvFile(args []string, getenv func(string) string, envPath string) (Config, error) {
	dotenv, err := loadDotEnv(envPath)
	if err != nil {
		return Config{}, err
	}
	// lookup layers process env over the .env file.
	lookup := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return dotenv[key]
	}

	fs := flag.NewFlagSet("goforge", flag.ContinueOnError)
	// Suppress the FlagSet's own usage dump on error; callers report the
	// returned error themselves (see cmd/goforge/main.go).
	fs.SetOutput(io.Discard)

	var cfg Config
	var mode string
	var allowCommands string
	var workdirs string
	var mcpServers string
	fs.StringVar(&cfg.Provider, "provider", "openai", "LLM provider: openai or anthropic")
	fs.StringVar(&cfg.Model, "model", "", "Model name (e.g., gpt-4o, claude-sonnet-4-20250514)")
	fs.StringVar(&cfg.BaseURL, "base-url", "", "Custom API base URL")
	fs.StringVar(&cfg.APIKey, "api-key", "", "API key (defaults to OPENAI_API_KEY or ANTHROPIC_API_KEY)")
	fs.StringVar(&cfg.System, "system", defaultSystem, "System prompt")
	fs.StringVar(&mode, "mode", string(ModeAgent), "Interactive mode: chat | tools | agent")
	fs.IntVar(&cfg.MaxSteps, "max-steps", defaultMaxSteps, "Max ReAct steps (agent mode only)")
	fs.DurationVar(&cfg.ToolTimeout, "tool-timeout", defaultToolTimeout, "Per-tool execution timeout (agent mode only)")
	fs.StringVar(&workdirs, "workdirs", "", "Comma-separated sandbox root directories for file/shell tools (default: current directory)")
	fs.StringVar(&allowCommands, "allow-commands", "", "Comma-separated shell command allowlist; empty disables exec_command")
	fs.StringVar(&mcpServers, "mcp-servers", "", "Comma-separated MCP server commands (stdio), e.g. \"npx -y @modelcontextprotocol/server-filesystem /path\"")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	// For any flag not explicitly set on the command line, fall back to the
	// environment (.env or process env). Explicit flags always win.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["provider"] {
		if v := lookup(envProvider); v != "" {
			cfg.Provider = v
		}
	}
	if !set["model"] {
		if v := lookup(envModel); v != "" {
			cfg.Model = v
		}
	}
	if !set["base-url"] {
		if v := lookup(envBaseURL); v != "" {
			cfg.BaseURL = v
		}
	}
	if !set["system"] {
		if v := lookup(envSystem); v != "" {
			cfg.System = v
		}
	}
	if !set["mode"] {
		if v := lookup(envMode); v != "" {
			mode = v
		}
	}
	if !set["max-steps"] {
		if v := lookup(envMaxSteps); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return Config{}, fmt.Errorf("invalid %s %q: %w", envMaxSteps, v, err)
			}
			cfg.MaxSteps = n
		}
	}
	if !set["tool-timeout"] {
		if v := lookup(envToolTimeout); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return Config{}, fmt.Errorf("invalid %s %q: %w", envToolTimeout, v, err)
			}
			cfg.ToolTimeout = d
		}
	}
	if !set["workdirs"] {
		if v := lookup(envWorkdir); v != "" {
			workdirs = v
		}
	}
	if !set["allow-commands"] {
		if v := lookup(envAllowCommands); v != "" {
			allowCommands = v
		}
	}
	if !set["mcp-servers"] {
		if v := lookup(envMCPServers); v != "" {
			mcpServers = v
		}
	}
	cfg.Workdirs = splitCommaList(workdirs)
	cfg.AllowCommands = splitCommaList(allowCommands)
	cfg.MCPServers = splitCommaList(mcpServers)

	cfg.Mode = Mode(mode)
	if !cfg.Mode.valid() {
		return Config{}, fmt.Errorf("invalid -mode %q: want chat, tools, or agent", mode)
	}

	// API key: explicit flag wins; otherwise resolve the provider-specific var
	// from process env or .env.
	if cfg.APIKey == "" {
		cfg.APIKey = lookup(apiKeyEnv(cfg.Provider))
	}

	return cfg, nil
}

// splitCommaList parses a comma-separated list into trimmed, non-empty items.
// Returns nil for an empty string so callers can treat "unset" uniformly.
func splitCommaList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// loadDotEnv reads a .env file of KEY=VALUE lines into a map. A missing file
// yields an empty map and no error. Blank lines and # comments are ignored, an
// optional leading "export " is stripped, and surrounding single/double quotes
// are removed from values. Malformed lines (no '=') are skipped.
func loadDotEnv(path string) (map[string]string, error) {
	m := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		m[key] = unquote(strings.TrimSpace(val))
	}
	return m, sc.Err()
}

// unquote strips a single pair of matching surrounding quotes, if present.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
