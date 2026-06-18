package workflow

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// wfLLM is a stateful fake that answers each stage by inspecting its system
// prompt, returning the structured JSON that stage parses. Review verdicts are
// consumed from a sequence (last repeats), letting tests drive the review
// back-edge deterministically.
type wfLLM struct {
	mu      sync.Mutex
	reviews []string
	ri      int
}

func (m *wfLLM) reply(system string) string {
	switch {
	case strings.Contains(system, "requirements analyst"):
		return `{"summary":"add Add","scope":["math"],"acceptance":[{"id":"AP-1","description":"Add(2,3)==5","kind":"unit"}]}`
	case strings.Contains(system, "software architect"):
		return `{"approach":"add a function","files":["math.go"],"risks":[]}`
	case strings.Contains(system, "software engineer"):
		return `{"files":["math.go"],"summary":"implemented Add"}`
	case strings.Contains(system, "code reviewer"):
		m.mu.Lock()
		defer m.mu.Unlock()
		v := m.reviews[min(m.ri, len(m.reviews)-1)]
		m.ri++
		return v
	case strings.Contains(system, "test engineer"):
		return `{"summary":"wrote tests"}`
	default:
		return `{}`
	}
}

func (m *wfLLM) Chat(_ context.Context, msgs []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	sys := ""
	if len(msgs) > 0 && msgs[0].Role == llm.RoleSystem {
		sys = msgs[0].Content
	}
	return &llm.Response{Message: llm.AssistantMessage(m.reply(sys)), StopReason: llm.StopReasonEnd, Usage: llm.Usage{TotalTokens: 1}}, nil
}

func (m *wfLLM) ChatStream(_ context.Context, msgs []llm.Message, _ ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: "", StopReason: llm.StopReasonEnd}, nil)
	}
}

// execScript is a fake exec_command tool returning canned outputs in sequence
// (last repeats). "[exit status N]" in the output signals a failed test layer.
type execScript struct {
	mu      sync.Mutex
	results []string
	i       int
}

type execArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func (e *execScript) toolFn(context.Context, execArgs) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.results[min(e.i, len(e.results)-1)]
	e.i++
	return r, nil
}

// fakeRegistry provides the codingTools by name (so per-stage tool filtering
// succeeds) with the fake exec_command wired to the given script.
func fakeRegistry(t *testing.T, exec *execScript) *tool.Registry {
	t.Helper()
	noop := func(name string) tool.Tool {
		return tool.NewTool(name, "fake", func(context.Context, struct{}) (string, error) { return "ok", nil })
	}
	reg := tool.NewRegistry()
	if err := reg.Register(
		noop("read_file"), noop("write_file"), noop("list_files"),
		tool.NewTool("exec_command", "fake exec", exec.toolFn),
	); err != nil {
		t.Fatal(err)
	}
	return reg
}

func runWorkflow(t *testing.T, llmc llm.LLM, reg *tool.Registry) (pipeline.EventType, map[string]int, *pipeline.Pipeline) {
	t.Helper()
	deps := pipeline.StageDeps{LLM: llmc, Registry: reg, State: pipeline.NewState()}
	p := BuildDevWorkflow(deps, Config{})
	enters := map[string]int{}
	var last pipeline.EventType = -1
	for ev, err := range p.Run(context.Background(), "wf-1", "Add an Add(a,b int) int function") {
		if err != nil {
			t.Fatalf("workflow error at %v/%s: %v (%s)", ev.Type, ev.Stage, err, ev.Detail)
		}
		if ev.Type == pipeline.EventStageEnter {
			enters[ev.Stage]++
		}
		last = ev.Type
	}
	return last, enters, p
}

func TestWorkflow_happyPath(t *testing.T) {
	exec := &execScript{results: []string{"ok"}} // every layer passes
	last, enters, p := runWorkflow(t, &wfLLM{reviews: []string{`{"pass":true,"reason":"clean"}`}}, fakeRegistry(t, exec))

	if last != pipeline.EventDone {
		t.Fatalf("workflow ended on %v, want Done", last)
	}
	// Each spine node ran exactly once on the happy path.
	for _, s := range []string{StageRequirement, StageTechDesign, StageCoding, StageReview, StageTestUnit, StageTestIntegr, StageTestE2E, StageAcceptance} {
		if enters[s] != 1 {
			t.Errorf("stage %s entered %d times, want 1", s, enters[s])
		}
	}
	if ok, unmet := allAcceptancePass(p.State()); !ok {
		t.Errorf("acceptance not all pass: %v", unmet)
	}
}

func TestWorkflow_reviewBounceBackToCoding(t *testing.T) {
	exec := &execScript{results: []string{"ok"}}
	// First review fails (→ coding), second passes.
	last, enters, _ := runWorkflow(t, &wfLLM{reviews: []string{`{"pass":false,"reason":"needs work"}`, `{"pass":true,"reason":"ok now"}`}}, fakeRegistry(t, exec))

	if last != pipeline.EventDone {
		t.Fatalf("ended on %v, want Done", last)
	}
	if enters[StageCoding] < 2 || enters[StageReview] < 2 {
		t.Errorf("expected a review→coding back-edge (coding=%d, review=%d)", enters[StageCoding], enters[StageReview])
	}
}

func TestWorkflow_testFailBouncesThenPasses(t *testing.T) {
	// Unit layer fails once (→ coding), then everything passes.
	exec := &execScript{results: []string{"FAIL\n[exit status 1]", "ok"}}
	last, enters, p := runWorkflow(t, &wfLLM{reviews: []string{`{"pass":true,"reason":"clean"}`}}, fakeRegistry(t, exec))

	if last != pipeline.EventDone {
		t.Fatalf("ended on %v, want Done", last)
	}
	if enters[StageTestUnit] < 2 || enters[StageCoding] < 2 {
		t.Errorf("expected a test_unit→coding back-edge (test_unit=%d, coding=%d)", enters[StageTestUnit], enters[StageCoding])
	}
	if ok, unmet := allAcceptancePass(p.State()); !ok {
		t.Errorf("acceptance should pass after rework: %v", unmet)
	}
}
