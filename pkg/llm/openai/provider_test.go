package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestChat_basicResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp-1",
			"object": "chat.completion",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hello!",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})
	}))
	defer srv.Close()

	p := New(Config{APIKey: "test-key", BaseURL: srv.URL + "/v1", Model: "gpt-4o"})
	resp, err := p.Chat(context.Background(), []llm.Message{llm.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Role != llm.RoleAssistant {
		t.Errorf("got role %q", resp.Message.Role)
	}
	if resp.Message.Content != "Hello!" {
		t.Errorf("got content %q", resp.Message.Content)
	}
	if resp.StopReason != llm.StopReasonEnd {
		t.Errorf("got stop_reason %q", resp.StopReason)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("got total_tokens %d", resp.Usage.TotalTokens)
	}
}

func TestChat_toolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp-2",
			"object": "chat.completion",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "calculator",
							"arguments": `{"a":1,"b":2}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{
				"prompt_tokens":     20,
				"completion_tokens": 10,
				"total_tokens":      30,
			},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "gpt-4o"})
	resp, err := p.Chat(context.Background(), []llm.Message{llm.UserMessage("calc 1+2")})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "calculator" || tc.Args != `{"a":1,"b":2}` {
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
			"error": map[string]any{
				"message": "invalid api key",
				"type":    "authentication_error",
			},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "gpt-4o"})
	_, err := p.Chat(context.Background(), []llm.Message{llm.UserMessage("hi")})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got == "" {
		t.Error("error message should not be empty")
	}
}

func TestChatStream_textDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		chunks := []map[string]any{
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}}}},
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "Hel"}}}},
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "lo!"}}}},
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
		}
		for _, c := range chunks {
			data, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "gpt-4o"})
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

func TestBuildParams_toolNamesInRequest(t *testing.T) {
	var sentNames []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, tool := range body.Tools {
			sentNames = append(sentNames, tool.Function.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Model: "gpt-4o"})
	_, err := p.Chat(context.Background(),
		[]llm.Message{llm.UserMessage("hi")},
		llm.WithTools(llm.ToolSchema{
			Name:        "km_corp_getArticleDetail",
			Description: "read KM article",
			Parameters:  map[string]any{"type": "object"},
		}),
	)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(sentNames) != 1 || sentNames[0] != "km_corp_getArticleDetail" {
		t.Fatalf("tool names sent to API = %v, want [km_corp_getArticleDetail]", sentNames)
	}
}

func TestConvertFinishReason(t *testing.T) {
	tests := []struct {
		input string
		want  llm.StopReason
	}{
		{"stop", llm.StopReasonEnd},
		{"tool_calls", llm.StopReasonToolCall},
		{"length", llm.StopReasonMaxTokens},
		{"unknown", llm.StopReasonEnd},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := convertFinishReason(tt.input); got != tt.want {
				t.Errorf("convertFinishReason(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
