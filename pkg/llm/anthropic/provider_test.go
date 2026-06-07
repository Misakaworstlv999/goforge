package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkanthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestChat_textResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "msg-1",
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "Hello!"},
			},
			"stop_reason": "end_turn",
			"model":       "claude-sonnet-4-20250514",
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	}))
	defer srv.Close()

	p := New(Config{APIKey: "test-key", BaseURL: srv.URL + "/v1", Model: "claude-sonnet-4-20250514"})
	resp, err := p.Chat(context.Background(), []llm.Message{
		llm.SystemMessage("be helpful"),
		llm.UserMessage("hi"),
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "Hello!" {
		t.Errorf("got content %q", resp.Message.Content)
	}
	if resp.StopReason != llm.StopReasonEnd {
		t.Errorf("got stop_reason %q", resp.StopReason)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("got total_tokens %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestChat_toolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "msg-2",
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "Let me calculate."},
				{"type": "tool_use", "id": "tu_1", "name": "calculator", "input": map[string]any{"a": 1.0, "b": 2.0}},
			},
			"stop_reason": "tool_use",
			"model":       "claude-sonnet-4-20250514",
			"usage": map[string]any{
				"input_tokens":  20,
				"output_tokens": 15,
			},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "claude-sonnet-4-20250514"})
	resp, err := p.Chat(context.Background(), []llm.Message{llm.UserMessage("calc 1+2")})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "Let me calculate." {
		t.Errorf("got content %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "tu_1" || tc.Name != "calculator" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if resp.StopReason != llm.StopReasonToolCall {
		t.Errorf("got stop_reason %q", resp.StopReason)
	}
}

func TestChat_apiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "authentication_error",
				"message": "invalid x-api-key",
			},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "claude-sonnet-4-20250514"})
	_, err := p.Chat(context.Background(), []llm.Message{llm.UserMessage("hi")})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatStream_textDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		events := []map[string]any{
			{"type": "message_start", "message": map[string]any{
				"id": "msg-1", "type": "message", "role": "assistant",
				"content": []any{}, "model": "claude-sonnet-4-20250514",
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
			}},
			{"type": "content_block_start", "index": 0, "content_block": map[string]any{
				"type": "text", "text": "",
			}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{
				"type": "text_delta", "text": "Hel",
			}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{
				"type": "text_delta", "text": "lo!",
			}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{
				"stop_reason": "end_turn",
			}, "usage": map[string]any{"output_tokens": 5}},
			{"type": "message_stop"},
		}
		for _, e := range events {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], data)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "claude-sonnet-4-20250514"})
	var collected string
	var lastStop llm.StopReason
	for chunk, err := range p.ChatStream(context.Background(), []llm.Message{llm.UserMessage("hi")}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		collected += chunk.Delta
		if chunk.StopReason != "" {
			lastStop = chunk.StopReason
		}
	}
	if collected != "Hello!" {
		t.Errorf("got %q, want %q", collected, "Hello!")
	}
	if lastStop != llm.StopReasonEnd {
		t.Errorf("got stop_reason %q", lastStop)
	}
}

func TestConvertStopReason(t *testing.T) {
	tests := []struct {
		input sdkanthropic.StopReason
		want  llm.StopReason
	}{
		{sdkanthropic.StopReasonEndTurn, llm.StopReasonEnd},
		{sdkanthropic.StopReasonToolUse, llm.StopReasonToolCall},
		{sdkanthropic.StopReasonMaxTokens, llm.StopReasonMaxTokens},
		{"unknown", llm.StopReasonEnd},
	}
	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := convertStopReason(tt.input); got != tt.want {
				t.Errorf("convertStopReason(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
