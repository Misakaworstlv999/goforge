package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// TestPipeline_durableLogCapturesFullTranscript proves the durable log stores the
// COMPLETE conversation (append-on-produce), not just compaction evictions — the
// store is the lossless source of truth (Q2).
func TestPipeline_durableLogCapturesFullTranscript(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	p := New(StageDeps{LLM: &scriptLLM{replies: []string{"the answer"}}}, WithStore(store))
	must(t, AddStage(p, Stage[string, string]{
		Name: "ask",
		Run: func(ctx context.Context, in string, deps StageDeps) (string, error) {
			return RunAgent(ctx, deps, "Q: "+in, "You are helpful.", agent.ContextPolicy{})
		},
	}))

	if _, err := collect(p.Run(ctx, "p1", "hello")); err != nil {
		t.Fatal(err)
	}

	hist, err := store.History(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	// system prompt + user task + assistant answer, in order.
	if len(hist) != 3 {
		t.Fatalf("transcript len = %d, want 3: %+v", len(hist), hist)
	}
	if hist[0].Role != llm.RoleSystem {
		t.Errorf("first message role = %s, want system", hist[0].Role)
	}
	if hist[1].Content != "Q: hello" || hist[2].Content != "the answer" {
		t.Errorf("transcript content wrong: %+v", hist)
	}
}

func TestHistorySource(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	must(t, store.AppendHistory(ctx, "p", []llm.Message{
		llm.UserMessage("first task"),
		llm.AssistantMessage("did the thing"),
	}))

	t.Run("renders whole transcript", func(t *testing.T) {
		msgs, err := HistorySource(store, "p", 0)(ctx, "ignored")
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("want 1 rendered context message, got %d", len(msgs))
		}
		body := msgs[0].Content
		if !strings.Contains(body, "first task") || !strings.Contains(body, "did the thing") {
			t.Errorf("history not rendered: %q", body)
		}
	})

	t.Run("limit keeps most recent", func(t *testing.T) {
		msgs, err := HistorySource(store, "p", 1)(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(msgs[0].Content, "first task") {
			t.Error("limit=1 should drop the oldest message")
		}
		if !strings.Contains(msgs[0].Content, "did the thing") {
			t.Error("limit=1 should keep the newest message")
		}
	})

	t.Run("absent pipeline yields nil", func(t *testing.T) {
		msgs, err := HistorySource(store, "absent", 0)(ctx, "")
		if err != nil || msgs != nil {
			t.Errorf("want nil msgs no err, got %v / %v", msgs, err)
		}
	})
}

// TestHistorySource_optInContinuity shows a downstream stage explicitly pulling
// the upstream transcript via its ContextPolicy (cross-stage continuity is opt-in).
func TestHistorySource_optInContinuity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	p := New(StageDeps{LLM: &scriptLLM{replies: []string{"upstream said X", "downstream done"}}}, WithStore(store))

	must(t, AddStage(p, Stage[string, string]{
		Name: "first",
		Run: func(ctx context.Context, in string, deps StageDeps) (string, error) {
			return RunAgent(ctx, deps, in, "first agent", agent.ContextPolicy{})
		},
	}))
	must(t, AddStage(p, Stage[string, string]{
		Name: "second",
		Run: func(ctx context.Context, in string, deps StageDeps) (string, error) {
			// opt in to the upstream conversation.
			pol := agent.ContextPolicy{Sources: []agent.ContextSource{HistorySource(store, "p1", 0)}}
			return RunAgent(ctx, deps, in, "second agent", pol)
		},
	}))

	if _, err := collect(p.Run(ctx, "p1", "start")); err != nil {
		t.Fatal(err)
	}
	// Both stages' transcripts accumulate under the same pipeline ID.
	hist, err := store.History(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	// Expect the second stage to have injected a HistorySource context message
	// (rendered "Prior pipeline conversation") in addition to its own system/task.
	var sawPrior bool
	for _, m := range hist {
		if strings.Contains(m.Content, "Prior pipeline conversation") {
			sawPrior = true
		}
	}
	if !sawPrior {
		t.Error("second stage should have injected the upstream transcript via HistorySource")
	}
}
