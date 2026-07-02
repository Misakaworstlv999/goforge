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
	// MCPConfigPath points to a standard mcpServers JSON file (Claude Desktop /
	// Cursor / .mcp.json format). Its servers' tools are discovered and
	// registered alongside the builtins. A missing file means no MCP servers.
	MCPConfigPath string
	// MCPExpose selects how MCP tools reach the agent: "direct" (each tool
	// registered individually) or "broker" (3 meta-tools for progressive
	// disclosure — better when there are many tools).
	MCPExpose string

	// --- Ring 5 service settings (consumed by the serve/run/status/resume
	// subcommands; the interactive REPL ignores them, so their defaults keep
	// today's behavior unchanged). ---

	// HTTPAddr is the listen address for `goforge serve`.
	HTTPAddr string
	// StorePath, when non-empty, opens a SQLite checkpoint store at this path so
	// runs persist across processes (required by status/resume). Empty ⇒ an
	// in-memory store (live only within one process).
	StorePath string
	// OTelEndpoint is the OTLP collector endpoint (host:port). Empty ⇒ telemetry
	// stays a no-op (nothing is exported). Never hardcode an endpoint; it must
	// come from flag/env.
	OTelEndpoint string
	// OTelInsecure sends OTLP over plaintext (no TLS) when true.
	OTelInsecure bool
	// ServiceName is the service.name resource attribute for telemetry.
	ServiceName string
	// OTelBody controls span payload capture: off | preview | full (default off).
	OTelBody string
	// OTelBodyMaxBytes caps captured body attributes (default 2048).
	OTelBodyMaxBytes int
	// LogLevel is the minimum structured-log level: debug|info|warn|error.
	LogLevel string
	// LogFormat selects the log encoding: console|json.
	LogFormat string
	// KMServer is the mcpServers key (from the MCP config) of a knowledge-base
	// MCP server whose doc-search tools are scoped onto the dev-workflow's
	// analysis stages. Empty ⇒ no KM (the workflow runs without doc lookup). The
	// concrete server name lives only in local config, never in tracked code.
	KMServer string
}

// Default values shared by Parse and tests.
const (
	defaultSystem           = "You are a helpful assistant. You can use tools when needed."
	defaultMaxSteps         = 10
	defaultToolTimeout      = 30 * time.Second
	defaultEnvFile          = ".env"
	defaultHTTPAddr         = ":8080"
	defaultServiceName      = "goforge"
	defaultOTelBodyMaxBytes = 2048
	defaultLogLevel         = "info"
	defaultLogFormat        = "console"
)

// Environment variable names. Non-secret app settings use a GOFORGE_ prefix;
// API keys keep their conventional provider-specific names.
const (
	envProvider         = "GOFORGE_PROVIDER"
	envModel            = "GOFORGE_MODEL"
	envBaseURL          = "GOFORGE_BASE_URL"
	envSystem           = "GOFORGE_SYSTEM"
	envMode             = "GOFORGE_MODE"
	envMaxSteps         = "GOFORGE_MAX_STEPS"
	envToolTimeout      = "GOFORGE_TOOL_TIMEOUT"
	envWorkdir          = "GOFORGE_WORKDIR"
	envAllowCommands    = "GOFORGE_ALLOW_COMMANDS"
	envMCPConfig        = "GOFORGE_MCP_CONFIG"
	envMCPExpose        = "GOFORGE_MCP_EXPOSE"
	envHTTPAddr         = "GOFORGE_HTTP_ADDR"
	envStore            = "GOFORGE_STORE"
	envOTelEndpoint     = "GOFORGE_OTEL_ENDPOINT"
	envOTelInsecure     = "GOFORGE_OTEL_INSECURE"
	envServiceName      = "GOFORGE_SERVICE_NAME"
	envOTelBody         = "GOFORGE_OTEL_BODY"
	envOTelBodyMaxBytes = "GOFORGE_OTEL_BODY_MAX_BYTES"
	envLogLevel         = "GOFORGE_LOG_LEVEL"
	envLogFormat        = "GOFORGE_LOG_FORMAT"
	envKMServer         = "GOFORGE_KM_SERVER"
)

const (
	// defaultMCPConfigPath is loaded if present when no path is given.
	defaultMCPConfigPath = ".mcp.json"

	// MCP tool exposure modes.
	MCPExposeDirect = "direct"
	MCPExposeBroker = "broker"
)

// Parse builds a Config from CLI args and an environment lookup function, also
// consulting a .env file in the current directory if present. getenv is injected
// (pass os.Getenv from main) so the function stays testable and free of global
// state.
func Parse(args []string, getenv func(string) string) (Config, error) {
	cfg, _, err := parseConfig(args, getenv, defaultEnvFile)
	return cfg, err
}

// ParseWithEnvFile is Parse with an explicit .env path, used by tests. A missing
// file is not an error. Precedence (low→high): defaults < .env < process env <
// flags. Process environment wins over .env (principle of least surprise).
func ParseWithEnvFile(args []string, getenv func(string) string, envPath string) (Config, error) {
	cfg, _, err := parseConfig(args, getenv, envPath)
	return cfg, err
}

// ParseArgs is Parse but also returns the leftover positional arguments (the
// task for `run`, the run id for `status`/`resume`). Flags must precede
// positionals, per the standard flag package.
func ParseArgs(args []string, getenv func(string) string) (Config, []string, error) {
	return parseConfig(args, getenv, defaultEnvFile)
}

// parseConfig is the shared parsing body returning the resolved config and the
// leftover positional args.
func parseConfig(args []string, getenv func(string) string, envPath string) (Config, []string, error) {
	dotenv, err := loadDotEnv(envPath)
	if err != nil {
		return Config{}, nil, err
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
	fs.StringVar(&cfg.MCPConfigPath, "mcp-config", defaultMCPConfigPath, "Path to a standard mcpServers JSON config (.mcp.json); missing file = no MCP servers")
	fs.StringVar(&cfg.MCPExpose, "mcp-expose", MCPExposeDirect, "How MCP tools reach the agent: direct | broker (progressive disclosure)")
	fs.StringVar(&cfg.HTTPAddr, "http", defaultHTTPAddr, "HTTP listen address for `goforge serve`")
	fs.StringVar(&cfg.StorePath, "store", "", "SQLite checkpoint store path; empty = in-memory (status/resume require a path)")
	fs.StringVar(&cfg.OTelEndpoint, "otel-endpoint", "", "OTLP collector endpoint (host:port); empty = telemetry disabled")
	fs.BoolVar(&cfg.OTelInsecure, "otel-insecure", false, "Send OTLP over plaintext (no TLS)")
	fs.StringVar(&cfg.ServiceName, "service-name", defaultServiceName, "service.name resource attribute for telemetry")
	fs.StringVar(&cfg.OTelBody, "otel-body", "", "Capture LLM/tool payloads on spans: off | preview | full")
	fs.IntVar(&cfg.OTelBodyMaxBytes, "otel-body-max-bytes", defaultOTelBodyMaxBytes, "Max bytes per captured span body attribute")
	fs.StringVar(&cfg.LogLevel, "log-level", defaultLogLevel, "Log level: debug | info | warn | error")
	fs.StringVar(&cfg.LogFormat, "log-format", defaultLogFormat, "Log format: console | json")
	fs.StringVar(&cfg.KMServer, "km-server", "", "mcpServers key of a knowledge-base MCP server for the dev-workflow analysis stages; empty = no KM")

	if err := fs.Parse(args); err != nil {
		return Config{}, nil, err
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
				return Config{}, nil, fmt.Errorf("invalid %s %q: %w", envMaxSteps, v, err)
			}
			cfg.MaxSteps = n
		}
	}
	if !set["tool-timeout"] {
		if v := lookup(envToolTimeout); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return Config{}, nil, fmt.Errorf("invalid %s %q: %w", envToolTimeout, v, err)
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
	if !set["mcp-config"] {
		if v := lookup(envMCPConfig); v != "" {
			cfg.MCPConfigPath = v
		}
	}
	if !set["mcp-expose"] {
		if v := lookup(envMCPExpose); v != "" {
			cfg.MCPExpose = v
		}
	}
	if !set["http"] {
		if v := lookup(envHTTPAddr); v != "" {
			cfg.HTTPAddr = v
		}
	}
	if !set["store"] {
		if v := lookup(envStore); v != "" {
			cfg.StorePath = v
		}
	}
	if !set["otel-endpoint"] {
		if v := lookup(envOTelEndpoint); v != "" {
			cfg.OTelEndpoint = v
		}
	}
	if !set["otel-insecure"] {
		if v := lookup(envOTelInsecure); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return Config{}, nil, fmt.Errorf("invalid %s %q: %w", envOTelInsecure, v, err)
			}
			cfg.OTelInsecure = b
		}
	}
	if !set["service-name"] {
		if v := lookup(envServiceName); v != "" {
			cfg.ServiceName = v
		}
	}
	if !set["otel-body"] {
		if v := lookup(envOTelBody); v != "" {
			cfg.OTelBody = v
		}
	}
	if !set["otel-body-max-bytes"] {
		if v := lookup(envOTelBodyMaxBytes); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return Config{}, nil, fmt.Errorf("invalid %s %q: %w", envOTelBodyMaxBytes, v, err)
			}
			cfg.OTelBodyMaxBytes = n
		}
	}
	if cfg.OTelBodyMaxBytes == 0 {
		cfg.OTelBodyMaxBytes = defaultOTelBodyMaxBytes
	}
	// OTelBody is validated where it is consumed (telemetry.ParseBodyCapture in
	// the serve/run wiring), keeping this low-level config package free of a
	// dependency on the telemetry/OTel-SDK layer.
	if !set["log-level"] {
		if v := lookup(envLogLevel); v != "" {
			cfg.LogLevel = v
		}
	}
	if !set["log-format"] {
		if v := lookup(envLogFormat); v != "" {
			cfg.LogFormat = v
		}
	}
	if !set["km-server"] {
		if v := lookup(envKMServer); v != "" {
			cfg.KMServer = v
		}
	}
	if cfg.MCPExpose != MCPExposeDirect && cfg.MCPExpose != MCPExposeBroker {
		return Config{}, nil, fmt.Errorf("invalid -mcp-expose %q: want direct or broker", cfg.MCPExpose)
	}
	cfg.Workdirs = splitCommaList(workdirs)
	cfg.AllowCommands = splitCommaList(allowCommands)

	cfg.Mode = Mode(mode)
	if !cfg.Mode.valid() {
		return Config{}, nil, fmt.Errorf("invalid -mode %q: want chat, tools, or agent", mode)
	}

	// API key: explicit flag wins; otherwise resolve the provider-specific var
	// from process env or .env.
	if cfg.APIKey == "" {
		cfg.APIKey = lookup(apiKeyEnv(cfg.Provider))
	}

	return cfg, fs.Args(), nil
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
