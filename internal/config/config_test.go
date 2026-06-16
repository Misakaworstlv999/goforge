package config

import (
	"testing"
	"time"
)

// noEnv is an empty environment lookup.
func noEnv(string) string { return "" }

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

func TestParse_invalidFlag(t *testing.T) {
	if _, err := Parse([]string{"-nope"}, noEnv); err == nil {
		t.Error("expected error for unknown flag")
	}
}
