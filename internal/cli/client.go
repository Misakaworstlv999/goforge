package cli

import (
	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/llm/anthropic"
	"github.com/Misakaworstlv999/goforge/pkg/llm/openai"
)

// Default models per provider, applied when cfg.Model is empty.
const (
	defaultOpenAIModel    = "gpt-5.4"
	defaultAnthropicModel = "claude-sonnet-4-6"
)

// NewLLM builds a provider-specific llm.LLM from config. It is the single
// composition point that turns configuration into a concrete client, so future
// edge entry points (HTTP API, MCP server in M7) can reuse it instead of
// re-implementing provider selection.
func NewLLM(cfg config.Config) llm.LLM {
	switch cfg.Provider {
	case "anthropic", "claude":
		model := cfg.Model
		if model == "" {
			model = defaultAnthropicModel
		}
		return anthropic.New(anthropic.Config{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   model,
		})
	default:
		model := cfg.Model
		if model == "" {
			model = defaultOpenAIModel
		}
		return openai.New(openai.Config{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   model,
		})
	}
}
