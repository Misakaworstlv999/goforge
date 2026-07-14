package workflow

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// capturingLLM records the messages of its most recent Chat call and returns a
// fixed reply, so a test can inspect what the coding agent actually saw.
type capturingLLM struct {
	reply    string
	lastMsgs []llm.Message
}

func (c *capturingLLM) Chat(_ context.Context, msgs []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	c.lastMsgs = msgs
	return &llm.Response{Message: llm.AssistantMessage(c.reply), StopReason: llm.StopReasonEnd, Usage: llm.Usage{TotalTokens: 1}}, nil
}

func (c *capturingLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: c.reply, StopReason: llm.StopReasonEnd}, nil)
	}
}

// TestCodingStage_reworkResumesConversation proves the rework loop resumes the
// coding agent's own prior conversation and appends the failure feedback, rather
// than restarting blind.
func TestCodingStage_reworkResumesConversation(t *testing.T) {
	st := pipeline.NewState()
	st.Set(designKey, Design{Approach: "impl it", Files: []string{"main.go"}})
	llmc := &capturingLLM{reply: `{"summary":"first impl","files":["main.go"]}`}
	deps := pipeline.StageDeps{LLM: llmc, Registry: tool.NewRegistry(), State: st, MaxAgentSteps: 2}
	stage := NewCodingStage(Config{})
	ctx := context.Background()

	// First pass: fresh seeding (system + task only).
	if _, err := stage.Run(ctx, "", deps); err != nil {
		t.Fatal(err)
	}
	freshLen := len(llmc.lastMsgs)
	if convo, ok := getArtifact[[]llm.Message](st, codingConvoKey); !ok || len(convo) == 0 {
		t.Fatal("first pass stored no coding conversation")
	}

	// Review rejects it → back-edge to coding.
	st.Set(reviewKey, reviewVerdict{Pass: false, Reason: "rename Foo to Bar"})

	// Second pass: rework — must resume the prior conversation + append feedback.
	if _, err := stage.Run(ctx, "", deps); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for _, m := range llmc.lastMsgs {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	seen := b.String()
	if !strings.Contains(seen, "rename Foo to Bar") {
		t.Errorf("rework prompt missing the review feedback:\n%s", seen)
	}
	if !strings.Contains(seen, "first impl") {
		t.Errorf("rework did not resume the prior conversation (prior turn absent):\n%s", seen)
	}
	if len(llmc.lastMsgs) <= freshLen {
		t.Errorf("rework conversation (%d msgs) should extend the fresh one (%d)", len(llmc.lastMsgs), freshLen)
	}
}

// TestCodingStage_firstPassIsFresh confirms the first entry (no failure signals)
// does not resume anything.
func TestCodingStage_firstPassIsFresh(t *testing.T) {
	if _, isRework := reworkFeedback(pipeline.NewState()); isRework {
		t.Error("a clean blackboard must not be treated as rework")
	}
}
