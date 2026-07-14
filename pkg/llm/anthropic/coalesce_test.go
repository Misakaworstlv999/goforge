package anthropic

import (
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
)

var userRole = sdkanthropic.NewUserMessage(sdkanthropic.NewTextBlock("x")).Role

// TestConvertMessages_coalescesAdjacentUsers checks the Anthropic alternation
// fix: a tool result (→ user message) immediately followed by an injected user
// turn (steer / feedback / subagent notice) is merged into ONE user turn holding
// both blocks, rather than emitting two consecutive user messages (which the API
// rejects).
func TestConvertMessages_coalescesAdjacentUsers(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "id1", Name: "t", Args: "{}"}}},
		llm.ToolMessage("id1", "the tool result", false),
		llm.UserMessage("[steer] be careful"),
	}
	api, _ := convertMessages(msgs)

	// assistant turn, then a single merged user turn (tool_result + steer text).
	if len(api) != 2 {
		t.Fatalf("got %d api messages, want 2 (assistant + merged user)", len(api))
	}
	if api[1].Role != userRole {
		t.Errorf("second message role = %v, want user", api[1].Role)
	}
	if len(api[1].Content) != 2 {
		t.Errorf("merged user turn has %d content blocks, want 2 (tool_result + text)", len(api[1].Content))
	}
}

func TestConvertMessages_mergesConsecutiveUsers(t *testing.T) {
	api, _ := convertMessages([]llm.Message{llm.UserMessage("a"), llm.UserMessage("b")})
	if len(api) != 1 || len(api[0].Content) != 2 {
		t.Fatalf("two user messages should merge to 1 with 2 blocks; got %d msgs", len(api))
	}
}

func TestConvertMessages_preservesAlternation(t *testing.T) {
	api, _ := convertMessages([]llm.Message{
		llm.UserMessage("hi"),
		llm.AssistantMessage("hello"),
		llm.UserMessage("more"),
	})
	if len(api) != 3 {
		t.Errorf("proper alternation must be left intact: got %d, want 3", len(api))
	}
}
