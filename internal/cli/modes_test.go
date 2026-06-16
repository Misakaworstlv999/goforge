package cli

import (
	"bytes"
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// mockLLM returns scripted responses in order (one per Chat call) and streams a
// fixed text for ChatStream. It implements llm.LLM for driving App in tests.
type mockLLM struct {
	responses []llm.Response
	calls     int
	stream    string
}

func (m *mockLLM) Chat(_ context.Context, _ []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	if m.calls >= len(m.responses) {
		return &llm.Response{Message: llm.AssistantMessage("done"), StopReason: llm.StopReasonEnd}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return &resp, nil
}

func (m *mockLLM) ChatStream(_ context.Context, _ []llm.Message, _ ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: m.stream}, nil)
	}
}

// runApp drives an App in the given mode over a single input line, returning the
// rendered output.
func runApp(t *testing.T, mode config.Mode, client llm.LLM) string {
	t.Helper()
	cfg := config.Config{Mode: mode, System: "sys", MaxSteps: 5}
	var out bytes.Buffer
	app := newWithClient(cfg, client, &out)
	in := strings.NewReader("hi\nexit\n")
	if err := app.Run(context.Background(), in); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	return out.String()
}

func TestApp_chatMode(t *testing.T) {
	out := runApp(t, config.ModeChat, &mockLLM{stream: "streamed reply"})
	if !strings.Contains(out, "streamed reply") {
		t.Errorf("chat output missing streamed text: %q", out)
	}
	if !strings.Contains(out, "Chat Mode") {
		t.Errorf("chat banner missing: %q", out)
	}
}

func TestApp_toolsMode(t *testing.T) {
	client := &mockLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Name: "calculator", Args: `{"a":2,"b":3,"op":"add"}`},
				},
			},
			StopReason: llm.StopReasonToolCall,
		},
		{Message: llm.AssistantMessage("the sum is 5"), StopReason: llm.StopReasonEnd},
	}}

	out := runApp(t, config.ModeTools, client)
	if !strings.Contains(out, "→ calculator") {
		t.Errorf("tools output missing tool call: %q", out)
	}
	if !strings.Contains(out, "✓ c1: 5") {
		t.Errorf("tools output missing tool result: %q", out)
	}
	if !strings.Contains(out, "the sum is 5") {
		t.Errorf("tools output missing final answer: %q", out)
	}
}

func TestApp_agentMode(t *testing.T) {
	client := &mockLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "let me compute",
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Name: "calculator", Args: `{"a":4,"b":5,"op":"add"}`},
				},
			},
			StopReason: llm.StopReasonToolCall,
		},
		{Message: llm.AssistantMessage("the answer is 9"), StopReason: llm.StopReasonEnd},
	}}

	out := runApp(t, config.ModeAgent, client)
	for _, want := range []string{"[think] let me compute", "→ calculator", "✓ c1: 9", "the answer is 9", "Agent Mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent output missing %q in:\n%s", want, out)
		}
	}
}
