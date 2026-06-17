package agent

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
)

// mockLLM returns scripted responses in order, one per Chat call. If err is
// non-nil it is returned immediately on the first call. It records the messages
// it was last called with for assertions.
type mockLLM struct {
	responses []llm.Response
	err       error
	calls     int
	lastMsgs  []llm.Message
}

func (m *mockLLM) Chat(_ context.Context, messages []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	m.lastMsgs = messages
	if m.err != nil {
		return nil, m.err
	}
	if m.calls >= len(m.responses) {
		// Default to a terminal response so a loop never blocks the test.
		return &llm.Response{
			Message:    llm.AssistantMessage("done"),
			StopReason: llm.StopReasonEnd,
		}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return &resp, nil
}

func (m *mockLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {}
}

// collect drains the agent iterator into slices of events and the terminal error.
func collect(t *testing.T, seq iter.Seq2[Event, error]) ([]Event, error) {
	t.Helper()
	var events []Event
	var lastErr error
	for ev, err := range seq {
		events = append(events, ev)
		if err != nil {
			lastErr = err
		}
	}
	return events, lastErr
}

func types(events []Event) []EventType {
	ts := make([]EventType, len(events))
	for i, e := range events {
		ts[i] = e.Type
	}
	return ts
}

func newCalcRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(builtin.NewCalculator()); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestSimpleAgent_toolCallThenFinal(t *testing.T) {
	mock := &mockLLM{responses: []llm.Response{
		{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "let me compute",
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Name: "calculator", Args: `{"a":2,"b":3,"op":"add"}`},
				},
			},
			StopReason: llm.StopReasonToolCall,
		},
		{
			Message:    llm.AssistantMessage("the answer is 5"),
			StopReason: llm.StopReasonEnd,
		},
	}}

	a := New(mock, newCalcRegistry(t), WithSystemPrompt("you are a helper"))
	events, err := collect(t, a.Run(context.Background(), "what is 2+3?"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []EventType{EventThink, EventToolCall, EventToolResult, EventThink, EventResponse}
	got := types(events)
	if len(got) != len(want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] type = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}

	// The tool result must reflect the calculator's output.
	if events[2].ToolResult == nil || events[2].ToolResult.Content != "5" {
		t.Errorf("tool result = %+v, want content 5", events[2].ToolResult)
	}
	// Final response content.
	if events[len(events)-1].Content != "the answer is 5" {
		t.Errorf("final response = %q", events[len(events)-1].Content)
	}
	// System prompt + user task should both be in the first call's messages.
	if len(mock.lastMsgs) < 2 || mock.lastMsgs[0].Role != llm.RoleSystem {
		t.Errorf("expected system prompt first, got %+v", mock.lastMsgs)
	}
}

func TestSimpleAgent_immediateFinal(t *testing.T) {
	mock := &mockLLM{responses: []llm.Response{
		{Message: llm.AssistantMessage("hi there"), StopReason: llm.StopReasonEnd},
	}}

	a := New(mock, tool.NewRegistry())
	events, err := collect(t, a.Run(context.Background(), "hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []EventType{EventThink, EventResponse}
	if got := types(events); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("event types = %v, want %v", got, want)
	}
}

func TestSimpleAgent_llmError(t *testing.T) {
	wantErr := errors.New("api down")
	mock := &mockLLM{err: wantErr}

	a := New(mock, tool.NewRegistry())
	events, err := collect(t, a.Run(context.Background(), "hello"))

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("expected single error event, got %v", types(events))
	}
}

func TestSimpleAgent_maxStepsExceeded(t *testing.T) {
	// Every response requests a tool call, so the loop never terminates naturally.
	toolResp := llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "calculator", Args: `{"a":1,"b":1,"op":"add"}`},
			},
		},
		StopReason: llm.StopReasonToolCall,
	}
	mock := &mockLLM{responses: []llm.Response{toolResp, toolResp, toolResp}}

	a := New(mock, newCalcRegistry(t), WithMaxSteps(2))
	events, err := collect(t, a.Run(context.Background(), "loop forever"))

	if !errors.Is(err, ErrMaxStepsExceeded) {
		t.Fatalf("err = %v, want ErrMaxStepsExceeded", err)
	}
	last := events[len(events)-1]
	if last.Type != EventError {
		t.Errorf("last event = %v, want EventError", last.Type)
	}
	// With maxSteps=2 and no Think text, each step emits ToolCall+ToolResult (2),
	// then a terminal error: 2*2 + 1 = 5 events.
	if len(events) != 5 {
		t.Errorf("got %d events, want 5: %v", len(events), types(events))
	}
}

func TestSimpleAgent_contextSourceInjected(t *testing.T) {
	mock := &mockLLM{responses: []llm.Response{
		{Message: llm.AssistantMessage("ok"), StopReason: llm.StopReasonEnd},
	}}
	src := StaticSource(llm.UserMessage("RETRIEVED CONTEXT"))
	a := New(mock, tool.NewRegistry(),
		WithSystemPrompt("sys"),
		WithContextPolicy(ContextPolicy{Sources: []ContextSource{src}}),
	)

	_, err := collect(t, a.Run(context.Background(), "do it"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Order must be: system, injected source, task.
	if len(mock.lastMsgs) < 3 {
		t.Fatalf("expected >=3 messages, got %+v", mock.lastMsgs)
	}
	if mock.lastMsgs[0].Role != llm.RoleSystem ||
		mock.lastMsgs[1].Content != "RETRIEVED CONTEXT" ||
		mock.lastMsgs[2].Content != "do it" {
		t.Errorf("unexpected message order: %+v", mock.lastMsgs)
	}
}

func TestSimpleAgent_contextSourceErrorAborts(t *testing.T) {
	mock := &mockLLM{}
	failing := func(context.Context, string) ([]llm.Message, error) {
		return nil, errors.New("retrieval down")
	}
	a := New(mock, tool.NewRegistry(),
		WithContextPolicy(ContextPolicy{Sources: []ContextSource{failing}}),
	)

	events, err := collect(t, a.Run(context.Background(), "task"))
	if err == nil {
		t.Fatal("expected error from failing source")
	}
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("expected single error event, got %v", types(events))
	}
	if mock.calls != 0 {
		t.Errorf("LLM should not be called when a source fails, got %d calls", mock.calls)
	}
}

func TestSimpleAgent_compactionInLoop(t *testing.T) {
	// Each step requests a tool (distinct call IDs) so the history grows across
	// rounds; a tiny MaxTokens forces compaction mid-loop. The run must complete
	// and the mock must never receive an orphaned tool_call/result.
	round := func(id string) llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role:      llm.RoleAssistant,
				Content:   "reasoning for step " + id,
				ToolCalls: []llm.ToolCall{{ID: id, Name: "calculator", Args: `{"a":1,"b":1,"op":"add"}`}},
			},
			StopReason: llm.StopReasonToolCall,
		}
	}
	final := llm.Response{Message: llm.AssistantMessage("final"), StopReason: llm.StopReasonEnd}
	mock := &validatingMockLLM{inner: mockLLM{responses: []llm.Response{
		round("c0"), round("c1"), round("c2"), round("c3"), final,
	}}}

	a := New(mock, newCalcRegistry(t),
		WithMaxSteps(8),
		WithContextPolicy(ContextPolicy{MaxTokens: 25, RetainRecent: 1}),
	)
	_, err := collect(t, a.Run(context.Background(), "loop"))
	if err != nil {
		t.Fatalf("unexpected error (pairing broke or compaction failed): %v", err)
	}
	if !mock.compactionObserved {
		t.Error("expected history to shrink at least once (compaction did not trigger)")
	}
}

// validatingMockLLM wraps mockLLM and asserts every request it receives has
// valid tool_call/tool_result pairing; it also notes when the message count
// drops between calls (evidence of compaction).
type validatingMockLLM struct {
	inner              mockLLM
	prevLen            int
	compactionObserved bool
}

func (m *validatingMockLLM) Chat(ctx context.Context, messages []llm.Message, opts ...llm.Option) (*llm.Response, error) {
	if m.prevLen > 0 && len(messages) < m.prevLen {
		m.compactionObserved = true
	}
	m.prevLen = len(messages)
	// Reject any request with broken tool pairing — mimics the real API's 400.
	if err := pairingError(messages); err != nil {
		return nil, err
	}
	return m.inner.Chat(ctx, messages, opts...)
}

func (m *validatingMockLLM) ChatStream(ctx context.Context, messages []llm.Message, opts ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return m.inner.ChatStream(ctx, messages, opts...)
}

func TestSimpleAgent_consumerBreaksEarly(t *testing.T) {
	mock := &mockLLM{responses: []llm.Response{
		{Message: llm.AssistantMessage("first"), StopReason: llm.StopReasonEnd},
	}}
	a := New(mock, tool.NewRegistry())

	// Break after the first event; the loop must stop without panic.
	count := 0
	for range a.Run(context.Background(), "hello") {
		count++
		break
	}
	if count != 1 {
		t.Errorf("expected to consume exactly 1 event before break, got %d", count)
	}
}
