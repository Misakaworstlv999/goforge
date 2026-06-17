package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestStaticSource(t *testing.T) {
	src := StaticSource(llm.UserMessage("ctx-a"), llm.AssistantMessage("ctx-b"))
	got, err := src(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "ctx-a" || got[1].Content != "ctx-b" {
		t.Errorf("unexpected source messages: %+v", got)
	}
}

// pairingError returns a non-nil error if any tool_call lacks its results or any
// tool result is orphaned — the invariant the OpenAI/Anthropic APIs enforce.
func pairingError(messages []llm.Message) error {
	for i, m := range messages {
		switch m.Role {
		case llm.RoleAssistant:
			if len(m.ToolCalls) == 0 {
				continue
			}
			answered := map[string]bool{}
			for j := i + 1; j < len(messages) && messages[j].Role == llm.RoleTool; j++ {
				answered[messages[j].ToolCallID] = true
			}
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					return fmt.Errorf("orphan tool_call %q (assistant msg %d) has no tool result", tc.ID, i)
				}
			}
		case llm.RoleTool:
			k := i - 1
			for k >= 0 && messages[k].Role == llm.RoleTool {
				k--
			}
			if k < 0 || messages[k].Role != llm.RoleAssistant {
				return fmt.Errorf("orphan tool result at %d: no owning assistant", i)
			}
			ok := false
			for _, tc := range messages[k].ToolCalls {
				if tc.ID == messages[i].ToolCallID {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("orphan tool result at %d: owning assistant lacks matching tool_call", i)
			}
		}
	}
	return nil
}

// assertPaired fails the test if pairing is broken.
func assertPaired(t *testing.T, messages []llm.Message) {
	t.Helper()
	if err := pairingError(messages); err != nil {
		t.Fatal(err)
	}
}

// buildHistory creates a multi-round history: system + task + N tool rounds + a
// final assistant message. Tool results are padded to be bulky.
func buildHistory(rounds int) []llm.Message {
	msgs := []llm.Message{
		llm.SystemMessage("you are helpful"),
		llm.UserMessage("ORIGINAL TASK"),
	}
	pad := strings.Repeat("x", 400)
	for i := range rounds {
		id := string(rune('a' + i))
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   "thinking step " + id,
			ToolCalls: []llm.ToolCall{{ID: id, Name: "calculator", Args: `{"a":1,"b":2,"op":"add"}`}},
		})
		msgs = append(msgs, llm.ToolMessage(id, "result "+id+" "+pad, false))
	}
	msgs = append(msgs, llm.AssistantMessage("final-ish answer"))
	return msgs
}

func TestSplitRegions_snapsTailOffToolResult(t *testing.T) {
	msgs := buildHistory(5)
	head, _, tail := splitRegions(msgs, 4)

	if len(head) == 0 || head[0].Role != llm.RoleSystem {
		t.Errorf("head should start with system: %+v", head)
	}
	if head[len(head)-1].Role != llm.RoleUser {
		t.Errorf("head should end at first user message")
	}
	// Tail must not begin with a tool result (would orphan it).
	if len(tail) > 0 && tail[0].Role == llm.RoleTool {
		t.Errorf("tail starts on an orphan tool result: %+v", tail[0])
	}
}

func TestDefaultCompact_pairingAndPreservation(t *testing.T) {
	msgs := buildHistory(6)
	before := NewEstimator().Count(msgs)
	budget := before / 3 // force escalation through the tiers

	out, err := DefaultCompact(context.Background(), &mockLLM{}, msgs, budget)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	// Correctness: no orphaned tool calls/results.
	assertPaired(t, out)

	// System + original task preserved at the head.
	if out[0].Role != llm.RoleSystem {
		t.Errorf("system message dropped")
	}
	foundTask := false
	for _, m := range out {
		if m.Role == llm.RoleUser && m.Content == "ORIGINAL TASK" {
			foundTask = true
		}
	}
	if !foundTask {
		t.Error("original task message dropped")
	}

	// Most recent message preserved (tail).
	if out[len(out)-1].Content != "final-ish answer" {
		t.Errorf("most recent message not preserved: %q", out[len(out)-1].Content)
	}

	// Compaction actually reduced the footprint.
	if NewEstimator().Count(out) >= before {
		t.Errorf("compaction did not reduce size: before=%d after=%d", before, NewEstimator().Count(out))
	}
}

func TestCompact_noBudgetIsNoop(t *testing.T) {
	msgs := buildHistory(3)
	out, err := DefaultCompact(context.Background(), &mockLLM{}, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(msgs) {
		t.Errorf("budget 0 should be a no-op, got %d want %d", len(out), len(msgs))
	}
}

func TestCompact_tinyHistoryBestEffort(t *testing.T) {
	// Only head + tail, nothing in the middle: returns as-is without error.
	msgs := []llm.Message{llm.SystemMessage("s"), llm.UserMessage("t")}
	out, err := compactMessages(context.Background(), &mockLLM{}, msgs, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("expected best-effort no-op, got %+v", out)
	}
}

func TestTruncateLongContent(t *testing.T) {
	long := strings.Repeat("a", 1000)
	out := truncateLongContent([]llm.Message{llm.AssistantMessage(long)}, 100)
	if !strings.Contains(out[0].Content, "truncated") || len([]rune(out[0].Content)) > 200 {
		t.Errorf("content not truncated: len=%d", len([]rune(out[0].Content)))
	}
}
