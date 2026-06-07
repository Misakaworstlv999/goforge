package llm

import (
	"context"
	"iter"
)

// LLM is the core abstraction for interacting with language models.
// Providers (OpenAI, Anthropic, etc.) implement this interface.
type LLM interface {
	// Chat sends messages and returns a complete response.
	Chat(ctx context.Context, messages []Message, opts ...Option) (*Response, error)

	// ChatStream sends messages and returns a streaming iterator.
	// Caller consumes via: for chunk, err := range llm.ChatStream(ctx, msgs) { ... }
	ChatStream(ctx context.Context, messages []Message, opts ...Option) iter.Seq2[Chunk, error]
}

// ToolSchema describes a tool that the LLM can invoke.
type ToolSchema struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}
