package pipeline

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// captureLLM records the messages of its most recent Chat call so a test can
// assert what actually reached the model, then returns a fixed final reply.
type captureLLM struct{ seen []llm.Message }

func (c *captureLLM) Chat(_ context.Context, msgs []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	c.seen = msgs
	return &llm.Response{
		Message:    llm.AssistantMessage("done"),
		StopReason: llm.StopReasonEnd,
		Usage:      llm.Usage{TotalTokens: 1},
	}, nil
}

func (c *captureLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: "done", StopReason: llm.StopReasonEnd}, nil)
	}
}

// TestSteerSource_reachesAgentPrompt proves bug #1 is fixed: guidance accumulated
// under SteerKey (by steer_run, or by a rewind/fork note) is surfaced into the
// agent's prompt via SteerSource, rather than sitting unread on the blackboard.
func TestSteerSource_reachesAgentPrompt(t *testing.T) {
	st := NewState()
	st.SetReducer(SteerKey, AppendReducer)
	st.Set(SteerKey, "avoid touching the auth module")

	cap := &captureLLM{}
	deps := StageDeps{LLM: cap, Registry: tool.NewRegistry(), State: st}

	// Empty policy: RunAgent must auto-inject SteerSource so every stage honors
	// steering without opting in.
	if _, err := RunAgent(context.Background(), deps, "do the task", "system prompt", agent.ContextPolicy{}); err != nil {
		t.Fatal(err)
	}

	var joined strings.Builder
	for _, m := range cap.seen {
		joined.WriteString(string(m.Role))
		joined.WriteString(": ")
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "avoid touching the auth module") {
		t.Errorf("steer guidance did not reach the agent prompt:\n%s", joined.String())
	}
}

func TestSteerSource_emptyInjectsNothing(t *testing.T) {
	msgs, err := SteerSource(NewState())(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if msgs != nil {
		t.Errorf("empty steer should inject nothing, got %v", msgs)
	}
}
