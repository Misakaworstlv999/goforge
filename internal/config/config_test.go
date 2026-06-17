package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noEnv is an empty environment lookup.
func noEnv(string) string { return "" }

// writeEnvFile writes content to a temp .env and returns its path.
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParse_defaults(t *testing.T) {
	cfg, err := Parse(nil, noEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModeAgent {
		t.Errorf("default Mode = %q, want %q", cfg.Mode, ModeAgent)
	}
	if cfg.Provider != "openai" {
		t.Errorf("default Provider = %q, want openai", cfg.Provider)
	}
	if cfg.MaxSteps != defaultMaxSteps {
		t.Errorf("default MaxSteps = %d, want %d", cfg.MaxSteps, defaultMaxSteps)
	}
	if cfg.ToolTimeout != defaultToolTimeout {
		t.Errorf("default ToolTimeout = %v, want %v", cfg.ToolTimeout, defaultToolTimeout)
	}
	if cfg.System != defaultSystem {
		t.Errorf("default System = %q", cfg.System)
	}
}

func TestParse_modes(t *testing.T) {
	tests := []struct {
		mode    string
		want    Mode
		wantErr bool
	}{
		{"chat", ModeChat, false},
		{"tools", ModeTools, false},
		{"agent", ModeAgent, false},
		{"bogus", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg, err := Parse([]string{"-mode", tt.mode}, noEnv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for mode %q", tt.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Mode != tt.want {
				t.Errorf("Mode = %q, want %q", cfg.Mode, tt.want)
			}
		})
	}
}

func TestParse_apiKeyFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantKey string
	}{
		{
			name:    "openai from env",
			args:    []string{"-provider", "openai"},
			env:     map[string]string{"OPENAI_API_KEY": "sk-openai"},
			wantKey: "sk-openai",
		},
		{
			name:    "anthropic from env",
			args:    []string{"-provider", "anthropic"},
			env:     map[string]string{"ANTHROPIC_API_KEY": "sk-anthropic"},
			wantKey: "sk-anthropic",
		},
		{
			name:    "explicit flag overrides env",
			args:    []string{"-provider", "openai", "-api-key", "sk-explicit"},
			env:     map[string]string{"OPENAI_API_KEY": "sk-env"},
			wantKey: "sk-explicit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			cfg, err := Parse(tt.args, getenv)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.APIKey != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.wantKey)
			}
		})
	}
}

func TestParse_overrides(t *testing.T) {
	cfg, err := Parse([]string{
		"-provider", "anthropic",
		"-model", "claude-x",
		"-base-url", "http://localhost:1234",
		"-system", "be terse",
		"-max-steps", "3",
		"-tool-timeout", "5s",
		"-mode", "tools",
	}, noEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-x" || cfg.BaseURL != "http://localhost:1234" {
		t.Errorf("provider/model/base-url not parsed: %+v", cfg)
	}
	if cfg.System != "be terse" || cfg.MaxSteps != 3 || cfg.ToolTimeout != 5*time.Second || cfg.Mode != ModeTools {
		t.Errorf("system/max-steps/tool-timeout/mode not parsed: %+v", cfg)
	}
}

func TestParse_sandboxFields(t *testing.T) {
	t.Run("flags", func(t *testing.T) {
		cfg, err := Parse([]string{"-workdirs", "/tmp/work,/tmp/docs", "-allow-commands", "echo, ls , , go"}, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		wantDirs := []string{"/tmp/work", "/tmp/docs"}
		if len(cfg.Workdirs) != len(wantDirs) {
			t.Fatalf("Workdirs = %v, want %v", cfg.Workdirs, wantDirs)
		}
		for i, w := range wantDirs {
			if cfg.Workdirs[i] != w {
				t.Errorf("Workdirs[%d] = %q, want %q", i, cfg.Workdirs[i], w)
			}
		}
		wantCmds := []string{"echo", "ls", "go"}
		if len(cfg.AllowCommands) != len(wantCmds) {
			t.Fatalf("AllowCommands = %v, want %v", cfg.AllowCommands, wantCmds)
		}
		for i, w := range wantCmds {
			if cfg.AllowCommands[i] != w {
				t.Errorf("AllowCommands[%d] = %q, want %q (trim + skip empties)", i, cfg.AllowCommands[i], w)
			}
		}
	})

	t.Run("default empty lists are nil", func(t *testing.T) {
		cfg, err := Parse(nil, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Workdirs != nil {
			t.Errorf("Workdirs = %v, want nil", cfg.Workdirs)
		}
		if cfg.AllowCommands != nil {
			t.Errorf("AllowCommands = %v, want nil", cfg.AllowCommands)
		}
	})

	t.Run("from env", func(t *testing.T) {
		env := writeEnvFile(t, "GOFORGE_WORKDIR=/srv/app,/srv/data\nGOFORGE_ALLOW_COMMANDS=cat,grep\n")
		cfg, err := ParseWithEnvFile(nil, noEnv, env)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Workdirs) != 2 || cfg.Workdirs[0] != "/srv/app" || cfg.Workdirs[1] != "/srv/data" {
			t.Errorf("Workdirs = %v", cfg.Workdirs)
		}
		if len(cfg.AllowCommands) != 2 || cfg.AllowCommands[0] != "cat" || cfg.AllowCommands[1] != "grep" {
			t.Errorf("AllowCommands = %v", cfg.AllowCommands)
		}
	})
}

func TestParse_mcpConfig(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg, _ := Parse(nil, noEnv)
		if cfg.MCPConfigPath != ".mcp.json" {
			t.Errorf("MCPConfigPath = %q, want default .mcp.json", cfg.MCPConfigPath)
		}
	})
	t.Run("flag", func(t *testing.T) {
		cfg, _ := Parse([]string{"-mcp-config", "/etc/mcp.json"}, noEnv)
		if cfg.MCPConfigPath != "/etc/mcp.json" {
			t.Errorf("MCPConfigPath = %q", cfg.MCPConfigPath)
		}
	})
	t.Run("env", func(t *testing.T) {
		env := writeEnvFile(t, "GOFORGE_MCP_CONFIG=/srv/mcp.json\n")
		cfg, _ := ParseWithEnvFile(nil, noEnv, env)
		if cfg.MCPConfigPath != "/srv/mcp.json" {
			t.Errorf("MCPConfigPath = %q", cfg.MCPConfigPath)
		}
	})
}

func TestParse_mcpExpose(t *testing.T) {
	t.Run("default direct", func(t *testing.T) {
		cfg, err := Parse(nil, noEnv)
		if err != nil || cfg.MCPExpose != MCPExposeDirect {
			t.Errorf("MCPExpose = %q err=%v, want direct", cfg.MCPExpose, err)
		}
	})
	t.Run("broker via flag", func(t *testing.T) {
		cfg, err := Parse([]string{"-mcp-expose", "broker"}, noEnv)
		if err != nil || cfg.MCPExpose != MCPExposeBroker {
			t.Errorf("MCPExpose = %q err=%v", cfg.MCPExpose, err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		if _, err := Parse([]string{"-mcp-expose", "nonsense"}, noEnv); err == nil {
			t.Error("expected error for invalid -mcp-expose")
		}
	})
}

func TestParse_invalidFlag(t *testing.T) {
	if _, err := Parse([]string{"-nope"}, noEnv); err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestParse_missingEnvFileIsOK(t *testing.T) {
	cfg, err := ParseWithEnvFile(nil, noEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing .env should not error: %v", err)
	}
	if cfg.Mode != ModeAgent {
		t.Errorf("Mode = %q, want default agent", cfg.Mode)
	}
}

func TestParse_dotEnvPopulatesFields(t *testing.T) {
	env := writeEnvFile(t, `
# GoForge config
GOFORGE_PROVIDER=anthropic
GOFORGE_MODEL="claude-x"
GOFORGE_MODE=tools
GOFORGE_MAX_STEPS=7
GOFORGE_TOOL_TIMEOUT=12s
export GOFORGE_BASE_URL='http://localhost:9999'
ANTHROPIC_API_KEY=sk-from-dotenv
`)
	cfg, err := ParseWithEnvFile(nil, noEnv, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-x" || cfg.Mode != ModeTools {
		t.Errorf("provider/model/mode from .env wrong: %+v", cfg)
	}
	if cfg.MaxSteps != 7 || cfg.ToolTimeout != 12*time.Second {
		t.Errorf("max-steps/tool-timeout from .env wrong: %+v", cfg)
	}
	if cfg.BaseURL != "http://localhost:9999" {
		t.Errorf("base-url (export + quotes) = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-from-dotenv" {
		t.Errorf("api key from .env = %q", cfg.APIKey)
	}
}

func TestParse_precedence(t *testing.T) {
	env := writeEnvFile(t, "GOFORGE_PROVIDER=anthropic\nGOFORGE_MODE=tools\nGOFORGE_MODEL=from-dotenv\n")

	// Process env overrides .env; explicit flag overrides both.
	procEnv := func(k string) string {
		if k == "GOFORGE_MODEL" {
			return "from-process-env"
		}
		return ""
	}
	cfg, err := ParseWithEnvFile([]string{"-mode", "agent"}, procEnv, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// flag wins for mode
	if cfg.Mode != ModeAgent {
		t.Errorf("Mode = %q, want agent (flag wins)", cfg.Mode)
	}
	// process env wins over .env for model
	if cfg.Model != "from-process-env" {
		t.Errorf("Model = %q, want from-process-env (process env > .env)", cfg.Model)
	}
	// .env provides provider (no flag, no process env)
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic (from .env)", cfg.Provider)
	}
}

func TestParse_invalidEnvValues(t *testing.T) {
	for _, tt := range []struct{ name, content string }{
		{"bad max-steps", "GOFORGE_MAX_STEPS=notanumber\n"},
		{"bad tool-timeout", "GOFORGE_TOOL_TIMEOUT=notaduration\n"},
		{"bad mode", "GOFORGE_MODE=bogus\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := writeEnvFile(t, tt.content)
			if _, err := ParseWithEnvFile(nil, noEnv, env); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestLoadDotEnv_parsing(t *testing.T) {
	env := writeEnvFile(t, "# comment\n\nKEY=value\n  SPACED  =  trimmed  \nQUOTED=\"q v\"\nNOEQUALS\nEMPTYKEY=\n")
	m, err := loadDotEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if m["KEY"] != "value" {
		t.Errorf("KEY = %q", m["KEY"])
	}
	if m["SPACED"] != "trimmed" {
		t.Errorf("SPACED = %q, want trimmed", m["SPACED"])
	}
	if m["QUOTED"] != "q v" {
		t.Errorf("QUOTED = %q, want 'q v'", m["QUOTED"])
	}
	if _, ok := m["NOEQUALS"]; ok {
		t.Error("malformed line (no '=') should be skipped")
	}
	if v, ok := m["EMPTYKEY"]; !ok || v != "" {
		t.Errorf("EMPTYKEY should map to empty string, got %q ok=%v", v, ok)
	}
}
