package workflow

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
)

// codeGenLLM is a tool-calling fake: the coding agent really invokes write_file
// (so real .go files land in the sandbox), other stages return JSON. If badCode
// is set, the FIRST coding pass writes it (buggy) and later passes write goodCode
// — driving a genuine test-failure → rework → pass cycle with real `go test`.
type codeGenLLM struct {
	mu                        sync.Mutex
	codingPass                int
	goodCode, badCode, tstSrc string
}

func (m *codeGenLLM) codeForThisPass() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codingPass++
	if m.codingPass == 1 && m.badCode != "" {
		return m.badCode
	}
	return m.goodCode
}

func (m *codeGenLLM) Chat(_ context.Context, msgs []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	sys := ""
	if len(msgs) > 0 && msgs[0].Role == llm.RoleSystem {
		sys = msgs[0].Content
	}
	final := func(s string) *llm.Response {
		return &llm.Response{Message: llm.AssistantMessage(s), StopReason: llm.StopReasonEnd, Usage: llm.Usage{TotalTokens: 1}}
	}

	switch {
	case strings.Contains(sys, "requirements analyst"):
		return final(`{"summary":"add Add","scope":["mathx"],"acceptance":[{"id":"AP-1","description":"Add(2,3)==5","kind":"unit"}]}`), nil
	case strings.Contains(sys, "software architect"):
		return final(`{"approach":"add a function","files":["math.go","math_test.go"],"risks":[]}`), nil
	case strings.Contains(sys, "software engineer"):
		// Finalize right after acting (last turn is a tool result); otherwise the
		// agent was just handed a task or rework feedback, so write the code. Keys
		// on the LAST turn (not "any tool result in history") so it behaves the
		// same whether the coding conversation is fresh or RESUMED with feedback.
		if lastIsToolResult(msgs) {
			return final(`{"files":["math.go","math_test.go"],"summary":"implemented Add"}`), nil
		}
		return &llm.Response{
			StopReason: llm.StopReasonToolCall,
			Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "w1", Name: "write_file", Args: writeArgs("math.go", m.codeForThisPass())},
				{ID: "w2", Name: "write_file", Args: writeArgs("math_test.go", m.tstSrc)},
			}},
		}, nil
	case strings.Contains(sys, "code reviewer"):
		return final(`{"pass":true,"reason":"looks good"}`), nil
	case strings.Contains(sys, "test engineer"):
		return final(`{"summary":"tests in place"}`), nil
	default:
		return final(`{}`), nil
	}
}

func (m *codeGenLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) { yield(llm.Chunk{StopReason: llm.StopReasonEnd}, nil) }
}

// lastIsToolResult reports whether the most recent message is a tool result —
// i.e. the agent just acted and should now finalize (vs. having just received a
// task/feedback turn to act on). Robust to a resumed conversation whose earlier
// history already contains tool results.
func lastIsToolResult(msgs []llm.Message) bool {
	return len(msgs) > 0 && msgs[len(msgs)-1].Role == llm.RoleTool
}

func writeArgs(path, content string) string {
	b, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return string(b)
}

const (
	addGood = "package mathx\n\nfunc Add(a, b int) int { return a + b }\n"
	addBad  = "package mathx\n\nfunc Add(a, b int) int { return a + b + 1 }\n" // off by one
	addTest = "package mathx\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", Add(2, 3))\n\t}\n}\n"
)

// scaffold creates a throwaway Go module the workflow will fill in and test.
func scaffold(t *testing.T) (root string, reg *tool.Registry) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module mathx\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := builtin.NewSandbox([]string{root}, builtin.WithAllowedCommands("go"), builtin.WithCommandTimeout(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reg = tool.NewRegistry()
	if err := reg.Register(builtin.NewReadFile(sb), builtin.NewWriteFile(sb), builtin.NewListFiles(sb), builtin.NewExecCommand(sb)); err != nil {
		t.Fatal(err)
	}
	return root, reg
}

func runToDone(t *testing.T, llmc llm.LLM, reg *tool.Registry, id, req string) *pipeline.Pipeline {
	t.Helper()
	deps := pipeline.StageDeps{LLM: llmc, Registry: reg, State: pipeline.NewState()}
	p := BuildDevWorkflow(deps, Config{})
	var last pipeline.EventType = -1
	for ev, err := range p.Run(context.Background(), id, req) {
		if err != nil {
			t.Fatalf("workflow error at %s: %v (%s)", ev.Stage, err, ev.Detail)
		}
		last = ev.Type
	}
	if last != pipeline.EventDone {
		t.Fatalf("workflow ended on %v, want Done", last)
	}
	return p
}

// TestE2E_realCodeRealGoTest is the end-to-end proof: agents write REAL Go files
// into a sandbox and the test stages run REAL `go test`; acceptance is backed by
// the actual exit code, not an LLM claim.
func TestE2E_realCodeRealGoTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e (runs real go test) in -short")
	}
	root, reg := scaffold(t)
	llmc := &codeGenLLM{goodCode: addGood, tstSrc: addTest}

	p := runToDone(t, llmc, reg, "e2e-happy", "Add an Add(a,b int) int function to package mathx")

	// Real artifacts on disk.
	if _, err := os.Stat(filepath.Join(root, "math.go")); err != nil {
		t.Errorf("math.go was not written: %v", err)
	}
	// Acceptance proven by the real test run.
	if ok, unmet := allAcceptancePass(p.State()); !ok {
		t.Errorf("acceptance not all pass: %v", unmet)
	}
	pts := getAcceptance(p.State())
	if len(pts) == 0 || pts[0].Status != StatusPass {
		t.Errorf("AP-1 not passed by real test: %+v", pts)
	}
}

// TestE2E_failureBouncesThenPasses proves the cyclic graph closes with REAL
// tests: the first implementation is buggy, real `go test` fails, the workflow
// routes back to coding, the fix is written, and acceptance then passes.
func TestE2E_failureBouncesThenPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e (runs real go test) in -short")
	}
	_, reg := scaffold(t)
	llmc := &codeGenLLM{goodCode: addGood, badCode: addBad, tstSrc: addTest}

	p := runToDone(t, llmc, reg, "e2e-rework", "Add a correct Add function to package mathx")

	if llmc.codingPass < 2 {
		t.Errorf("expected ≥2 coding passes (rework after a real test failure), got %d", llmc.codingPass)
	}
	if ok, unmet := allAcceptancePass(p.State()); !ok {
		t.Errorf("acceptance should pass after the fix: %v", unmet)
	}
}
