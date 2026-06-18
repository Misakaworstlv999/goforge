package pipeline

import (
	"context"
	"fmt"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
)

// Example wires a tiny three-stage pipeline: an agent-driven draft stage, an
// LLM review gate, and a pure finalize stage. It shows the typed Stage[In,Out]
// hand-off and the streaming event model. (Ring 5 CLI subcommands arrive in M7.)
func Example() {
	client := &scriptLLM{replies: []string{"cats are great", "PASS"}}
	p := New(StageDeps{LLM: client})

	_ = AddStage(p, Stage[string, string]{
		Name: "draft",
		Run: func(ctx context.Context, topic string, deps StageDeps) (string, error) {
			return RunAgent(ctx, deps, "Write a line about: "+topic, "You are a writer.", agent.ContextPolicy{})
		},
		Gate: LLMReviewGate(client, "must be on-topic"),
	})
	_ = AddStage(p, Stage[string, string]{
		Name: "finalize",
		Run:  func(_ context.Context, draft string, _ StageDeps) (string, error) { return "FINAL: " + draft, nil },
	})

	var final string
	for ev, err := range p.Run(context.Background(), "demo", "cats") {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		if ev.Type == EventStageOutput && ev.Stage == "finalize" {
			final = ev.Detail
		}
	}
	fmt.Println(final)
	// Output: FINAL: cats are great
}
