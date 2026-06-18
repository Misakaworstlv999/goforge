package pipeline

import (
	"context"
	"iter"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// scriptLLM is a minimal llm.LLM that returns canned assistant replies in
// sequence, repeating the last once exhausted. It never requests tools, so an
// agent driven by it terminates in one step.
type scriptLLM struct {
	replies []string
	i       int
}

func (m *scriptLLM) next() string {
	switch {
	case m.i < len(m.replies):
		r := m.replies[m.i]
		m.i++
		return r
	case len(m.replies) > 0:
		return m.replies[len(m.replies)-1]
	default:
		return ""
	}
}

func (m *scriptLLM) Chat(context.Context, []llm.Message, ...llm.Option) (*llm.Response, error) {
	return &llm.Response{
		Message:    llm.AssistantMessage(m.next()),
		StopReason: llm.StopReasonEnd,
		Usage:      llm.Usage{TotalTokens: 1},
	}, nil
}

func (m *scriptLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: m.next(), StopReason: llm.StopReasonEnd}, nil)
	}
}

// collect drains a pipeline event iterator into a slice of types plus the
// terminal error (nil on success/pause).
func collect(seq iter.Seq2[Event, error]) (events []Event, err error) {
	for ev, e := range seq {
		events = append(events, ev)
		if e != nil {
			err = e
		}
	}
	return events, err
}

// lastType returns the Type of the final event (or -1 if none).
func lastType(events []Event) EventType {
	if len(events) == 0 {
		return -1
	}
	return events[len(events)-1].Type
}
