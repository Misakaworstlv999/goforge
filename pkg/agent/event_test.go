package agent

import (
	"errors"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestEventType_String(t *testing.T) {
	tests := []struct {
		typ  EventType
		want string
	}{
		{EventThink, "think"},
		{EventToolCall, "tool_call"},
		{EventToolResult, "tool_result"},
		{EventResponse, "response"},
		{EventError, "error"},
		{EventType(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.typ.String(); got != tt.want {
				t.Errorf("EventType(%d).String() = %q, want %q", tt.typ, got, tt.want)
			}
		})
	}
}

func TestConstructors(t *testing.T) {
	usage := &llm.Usage{TotalTokens: 42}

	t.Run("think", func(t *testing.T) {
		ev := ThinkEvent("reasoning", usage, 1)
		if ev.Type != EventThink || ev.Content != "reasoning" || ev.Usage != usage || ev.Step != 1 {
			t.Errorf("unexpected think event: %+v", ev)
		}
		if ev.ToolCall != nil || ev.ToolResult != nil {
			t.Error("think event should not carry tool payloads")
		}
	})

	t.Run("tool_call", func(t *testing.T) {
		call := llm.ToolCall{ID: "c1", Name: "calculator", Args: `{"op":"add"}`}
		ev := ToolCallEvent(call, 2)
		if ev.Type != EventToolCall || ev.Step != 2 {
			t.Errorf("unexpected tool_call event: %+v", ev)
		}
		if ev.ToolCall == nil || ev.ToolCall.Name != "calculator" {
			t.Errorf("tool_call payload missing or wrong: %+v", ev.ToolCall)
		}
		if ev.Content != "calculator" {
			t.Errorf("Content = %q, want tool name", ev.Content)
		}
	})

	t.Run("tool_result", func(t *testing.T) {
		res := llm.ToolResult{CallID: "c1", Content: "7", IsError: false}
		ev := ToolResultEvent(res, 2)
		if ev.Type != EventToolResult || ev.ToolResult == nil || ev.ToolResult.Content != "7" {
			t.Errorf("unexpected tool_result event: %+v", ev)
		}
		if ev.Content != "7" {
			t.Errorf("Content = %q, want result content", ev.Content)
		}
	})

	t.Run("response", func(t *testing.T) {
		ev := ResponseEvent("final answer", usage, 3)
		if ev.Type != EventResponse || ev.Content != "final answer" || ev.Usage != usage {
			t.Errorf("unexpected response event: %+v", ev)
		}
	})

	t.Run("error", func(t *testing.T) {
		err := errors.New("boom")
		ev := ErrorEvent(err, 4)
		if ev.Type != EventError || ev.Content != "boom" || ev.Step != 4 {
			t.Errorf("unexpected error event: %+v", ev)
		}
	})
}
