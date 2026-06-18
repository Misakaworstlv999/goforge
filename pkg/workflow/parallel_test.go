package workflow

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// TestOrchestratorWorker_parallelCoding demonstrates M6-006: an orchestrator
// decomposes work into N worker agents that run concurrently (one ParallelStage
// node, transparent to the FSM), each writing branch-safely to a reducer-guarded
// blackboard key, with their outputs synthesized by join.
func TestOrchestratorWorker_parallelCoding(t *testing.T) {
	state := pipeline.NewState()
	state.SetReducer("files", pipeline.AppendReducer) // branch-safe accumulation
	deps := pipeline.StageDeps{
		LLM:      &jsonLLM{reply: `{"files":["x"],"summary":"ok"}`},
		Registry: tool.NewRegistry(),
		State:    state,
	}

	worker := func(name, file string) pipeline.Stage[string, string] {
		return pipeline.Stage[string, string]{
			Name: name,
			Run: func(ctx context.Context, _ string, d pipeline.StageDeps) (string, error) {
				if _, err := pipeline.RunAgent(ctx, d, "implement "+file, codingSystem, agent.ContextPolicy{}); err != nil {
					return "", err
				}
				d.State.Set("files", file)
				return file, nil
			},
		}
	}

	par := pipeline.ParallelStage("parallel_coding",
		[]pipeline.Stage[string, string]{worker("w1", "a.go"), worker("w2", "b.go"), worker("w3", "c.go")},
		func(outs []string) string { sort.Strings(outs); return strings.Join(outs, ",") },
	)

	p := pipeline.New(deps)
	if err := pipeline.AddStage(p, par); err != nil {
		t.Fatal(err)
	}

	var last pipeline.EventType = -1
	var detail string
	for ev, err := range p.Run(context.Background(), "ow-1", "decompose into 3 files") {
		if err != nil {
			t.Fatalf("workflow error: %v", err)
		}
		if ev.Type == pipeline.EventStageOutput {
			detail = ev.Detail
		}
		last = ev.Type
	}
	if last != pipeline.EventDone {
		t.Fatalf("want Done, got %v", last)
	}
	if detail != "a.go,b.go,c.go" {
		t.Errorf("join synthesis = %q, want a.go,b.go,c.go", detail)
	}
	if acc, _ := state.Get("files"); func() bool { a, ok := acc.([]any); return !ok || len(a) != 3 }() {
		t.Errorf("expected 3 concurrent worker writes, got %#v", acc)
	}
}
