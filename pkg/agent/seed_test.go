package agent

import (
	"context"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// TestRun_seedResumesConversation checks WithSeedMessages: the agent starts from
// the prior conversation, appends the task as the next turn, does NOT re-seed the
// system prompt, and reports the full conversation via WithConversationSink.
func TestRun_seedResumesConversation(t *testing.T) {
	seed := []llm.Message{
		llm.SystemMessage("SYS"),
		llm.UserMessage("original task"),
		llm.AssistantMessage("my first attempt"),
	}
	mock := &mockLLM{} // defaults to a terminal "done" response

	var captured []llm.Message
	a := New(mock, tool.NewRegistry(),
		WithSystemPrompt("SYS"),
		WithSeedMessages(seed),
		WithConversationSink(func(m []llm.Message) { captured = m }),
	)

	_, err := collect(t, a.Run(context.Background(), "review feedback: fix X"))
	if err != nil {
		t.Fatal(err)
	}

	// The model saw seed (3) + the appended feedback user turn (1) = 4 messages,
	// and exactly one system message (from the seed, not re-added).
	if len(mock.lastMsgs) != 4 {
		t.Fatalf("model saw %d messages, want 4: %+v", len(mock.lastMsgs), mock.lastMsgs)
	}
	sysCount := 0
	for _, m := range mock.lastMsgs {
		if m.Role == llm.RoleSystem {
			sysCount++
		}
	}
	if sysCount != 1 {
		t.Errorf("system messages = %d, want 1 (seed's, not re-added)", sysCount)
	}
	last := mock.lastMsgs[len(mock.lastMsgs)-1]
	if last.Role != llm.RoleUser || last.Content != "review feedback: fix X" {
		t.Errorf("last message = %+v, want the feedback user turn", last)
	}
	// The conversation sink reports seed + feedback + the assistant reply.
	if len(captured) < 4 {
		t.Errorf("conversation sink got %d messages, want >= 4", len(captured))
	}
}

// TestRun_noSeedIsUnchanged confirms the zero-value path (no seed) still seeds
// system + task fresh.
func TestRun_noSeedIsUnchanged(t *testing.T) {
	mock := &mockLLM{}
	a := New(mock, tool.NewRegistry(), WithSystemPrompt("SYS"))
	if _, err := collect(t, a.Run(context.Background(), "do it")); err != nil {
		t.Fatal(err)
	}
	if len(mock.lastMsgs) != 2 || mock.lastMsgs[0].Role != llm.RoleSystem || mock.lastMsgs[1].Content != "do it" {
		t.Errorf("fresh seeding changed: %+v", mock.lastMsgs)
	}
}
