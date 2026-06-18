package workflow

import (
	"context"
	"iter"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// jsonLLM is a minimal llm.LLM returning a fixed reply (typically a JSON object),
// with no tool calls, so an agent driven by it terminates in one step. Used for
// unit-testing stages that parse structured LLM output.
type jsonLLM struct{ reply string }

func (m *jsonLLM) Chat(context.Context, []llm.Message, ...llm.Option) (*llm.Response, error) {
	return &llm.Response{
		Message:    llm.AssistantMessage(m.reply),
		StopReason: llm.StopReasonEnd,
		Usage:      llm.Usage{TotalTokens: 1},
	}, nil
}

func (m *jsonLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: m.reply, StopReason: llm.StopReasonEnd}, nil)
	}
}
