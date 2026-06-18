package pipeline_test

// Full-stack integration test: a real pipeline running across all rings with
// ONLY the model faked (via httptest, per AGENTS.md — never hit a real API).
//
// Real components exercised together:
//   - Ring 1: openai.Provider doing real HTTP + type conversion against a fake
//     OpenAI server.
//   - Ring 2: tool.Registry + the builtin calculator, executed for real.
//   - Ring 3: SimpleAgent's ReAct loop (tool call → execute → observe → answer)
//     plus the transcript sink.
//   - Ring 4: Pipeline FSM routing, a HumanGate pause, SQLite checkpoint on a
//     temp file, interrupt → (simulated process restart) → resume, audit, and
//     the durable transcript.
//
// It uses only the package's exported API (black-box), proving a consumer can
// actually build and run this.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/llm/openai"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
)

// fakeOpenAI emulates the chat-completions endpoint: it asks for a calculator
// tool call until it sees a tool result in the request, then answers. This drives
// the agent through a full ReAct cycle deterministically.
func fakeOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		w.Header().Set("Content-Type", "application/json")

		// A tool result has been sent back ⇒ produce the final answer.
		if bytes.Contains(body, []byte("tool_call_id")) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r-final", "object": "chat.completion",
				"choices": []map[string]any{{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "The sum is 5."},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10},
			})
			return
		}
		// Otherwise, request a calculator call.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r-tool", "object": "chat.completion",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "calculator", "arguments": `{"a":2,"b":3,"op":"add"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12},
		})
	}))
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

// buildPipeline wires the same three-stage pipeline used before and after the
// simulated restart: compute (agent + calculator) → review (human gate) →
// finalize. deps/state are fresh each call; the store is shared via the DB file.
func buildPipeline(llmc llm.LLM, reg *tool.Registry, store pipeline.CheckpointStore) *pipeline.Pipeline {
	p := pipeline.New(pipeline.StageDeps{LLM: llmc, Registry: reg}, pipeline.WithStore(store))

	_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
		Name:  "compute",
		Tools: []string{"calculator"}, // per-stage tool subset
		Run: func(ctx context.Context, in string, deps pipeline.StageDeps) (string, error) {
			ans, err := pipeline.RunAgent(ctx, deps, "Add 2 and 3 with the calculator.",
				"You are a precise calculator agent. Use the tool.", agent.ContextPolicy{})
			if err != nil {
				return "", err
			}
			deps.State.Set("sum", ans) // share via blackboard
			return ans, nil
		},
	})
	_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
		Name: "review",
		Run:  func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) { return in, nil },
		Gate: pipeline.HumanGate(),
	})
	_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
		Name: "finalize",
		Run:  func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) { return "DONE: " + in, nil },
	})
	return p
}

func TestIntegration_fullStackWithRestartAndResume(t *testing.T) {
	srv := fakeOpenAI(t)
	defer srv.Close()

	provider := openai.New(openai.Config{BaseURL: srv.URL + "/v1", Model: "gpt-4o", APIKey: "test"})
	reg := tool.NewRegistry()
	if err := reg.Register(builtin.NewCalculator()); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "pipe.db")
	const id = "job-1"

	// --- Run 1: pause at the human gate, persisting to SQLite. ---
	store1, err := pipeline.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	last, lastDetail := drain(t, buildPipeline(provider, reg, store1).Run(context.Background(), id, "start"))
	if last != pipeline.EventPaused {
		t.Fatalf("run 1 ended on %v (%q), want Paused", last, lastDetail)
	}

	// The agent really called the tool: compute's output flowed to the gate.
	st, err := store1.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != pipeline.StatusPaused || st.CurrentStage != "review" {
		t.Fatalf("persisted state wrong: status=%v stage=%q", st.Status, st.CurrentStage)
	}
	if st.Blackboard["sum"] != "The sum is 5." {
		t.Errorf("blackboard 'sum' = %v, want 'The sum is 5.'", st.Blackboard["sum"])
	}
	_ = store1.Close() // simulate process exit

	// --- Simulated restart: reopen the SAME db file in a fresh pipeline. ---
	store2, err := pipeline.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	// The durable transcript written by run 1 survived the restart, in full:
	// system + task + assistant(tool_call) + tool result "5" + final answer.
	hist, err := store2.History(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !hasToolResult(hist, "5") {
		t.Errorf("durable transcript missing the tool result '5': %+v", hist)
	}
	if !hasContent(hist, "The sum is 5.") {
		t.Errorf("durable transcript missing the final answer: %+v", hist)
	}

	// --- Resume (approve) on the fresh pipeline → runs finalize → completes. ---
	p2 := buildPipeline(provider, reg, store2)
	var finalizeOut string
	var ended pipeline.EventType = -1
	for ev, err := range p2.Resume(context.Background(), id, pipeline.Decision{Approved: true}) {
		if err != nil {
			t.Fatalf("resume error at %v: %v", ev.Type, err)
		}
		if ev.Type == pipeline.EventStageOutput && ev.Stage == "finalize" {
			finalizeOut = ev.Detail
		}
		ended = ev.Type
	}
	if ended != pipeline.EventDone {
		t.Fatalf("resume ended on %v, want Done", ended)
	}
	// End-to-end result: compute("The sum is 5.") → review pass-through → finalize.
	if finalizeOut != "DONE: The sum is 5." {
		t.Errorf("final output = %q, want %q", finalizeOut, "DONE: The sum is 5.")
	}

	// Final FSM state persisted as completed; blackboard restored across restart.
	st, err = store2.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != pipeline.StatusCompleted {
		t.Errorf("final status = %v, want Completed", st.Status)
	}
	if got, _ := p2.State().Get("sum"); got != "The sum is 5." {
		t.Errorf("blackboard not restored on resume: sum=%v", got)
	}

	// Audit spans both store instances: interrupt (run 1) then resume + complete (run 2).
	log, err := store2.AuditLog(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAction(log, pipeline.ActionInterrupt) || !hasAction(log, pipeline.ActionResume) || !hasAction(log, pipeline.ActionComplete) {
		t.Errorf("audit log missing interrupt/resume/complete: %+v", actions(log))
	}
}

// --- small black-box helpers ---

func drain(t *testing.T, seq func(func(pipeline.Event, error) bool)) (pipeline.EventType, string) {
	t.Helper()
	var lastType pipeline.EventType = -1
	var detail string
	for ev, err := range seq {
		if err != nil {
			t.Fatalf("pipeline error at %v: %v", ev.Type, err)
		}
		lastType, detail = ev.Type, ev.Detail
	}
	return lastType, detail
}

func hasToolResult(msgs []llm.Message, content string) bool {
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.Content == content {
			return true
		}
	}
	return false
}

func hasContent(msgs []llm.Message, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

func hasAction(log []pipeline.AuditEntry, action string) bool {
	for _, e := range log {
		if e.Action == action {
			return true
		}
	}
	return false
}

func actions(log []pipeline.AuditEntry) []string {
	out := make([]string, len(log))
	for i, e := range log {
		out[i] = e.Action
	}
	return out
}
