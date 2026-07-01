package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

func TestRenderTranscript_levels(t *testing.T) {
	msgs := []llm.Message{
		llm.SystemMessage("SYSPROMPT"),
		llm.UserMessage("task text"),
		{Role: llm.RoleAssistant, Content: "reasoning here", ToolCalls: []llm.ToolCall{{ID: "1", Name: "toolA", Args: "ARGSBODY"}}},
		llm.ToolMessage("1", "RESULTBODY", true),
		{Role: llm.RoleAssistant, Content: "final answer"},
	}

	full := RenderTranscript(msgs, "full")
	for _, want := range []string{"SYSPROMPT", "ARGSBODY", "RESULTBODY", "ERROR", "reasoning here"} {
		if !strings.Contains(full, want) {
			t.Errorf("full transcript missing %q:\n%s", want, full)
		}
	}

	steps := RenderTranscript(msgs, "steps")
	if strings.Contains(steps, "SYSPROMPT") || strings.Contains(steps, "ARGSBODY") {
		t.Errorf("steps level should omit system prompt and tool args:\n%s", steps)
	}
	if !strings.Contains(steps, "toolA") || !strings.Contains(steps, "reasoning here") {
		t.Errorf("steps level missing reasoning/tool name:\n%s", steps)
	}

	if got := strings.TrimSpace(RenderTranscript(msgs, "final")); got != "final answer" {
		t.Errorf("final level = %q, want just the last answer", got)
	}
	if RenderTranscript(msgs, "bogus") != steps {
		t.Error("unknown level should behave as steps")
	}
}

func TestControlTools_getTranscript(t *testing.T) {
	var mu sync.Mutex
	var rec []string
	mgr, store := managerFixture(t, nil, &rec, &mu)
	byName := map[string]tool.Tool{}
	for _, tl := range ControlTools(mgr) {
		byName[tl.Name()] = tl
	}
	ctx := context.Background()

	id, _ := mgr.Trigger("tr", "x")
	mgr.Wait(id)
	// Seed a transcript for the run (echo stage produces none on its own).
	if err := store.AppendHistory(ctx, id, []llm.Message{
		llm.UserMessage("do the thing"),
		{Role: llm.RoleAssistant, Content: "calling a tool", ToolCalls: []llm.ToolCall{{ID: "1", Name: "calc", Args: `{"x":1}`}}},
		llm.ToolMessage("1", "result=2", false),
		{Role: llm.RoleAssistant, Content: "the answer is 2"},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := byName["get_run_transcript"].Execute(ctx, []byte(`{"run_id":"tr","level":"steps"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do the thing") || !strings.Contains(out, "tool_call calc") || !strings.Contains(out, "the answer is 2") {
		t.Errorf("steps transcript missing expected content:\n%s", out)
	}
	if strings.Contains(out, `{"x":1}`) {
		t.Errorf("steps level must not leak tool args:\n%s", out)
	}
}

func TestControlTools_triggerObserve(t *testing.T) {
	var mu sync.Mutex
	var rec []string
	mgr, _ := managerFixture(t, nil, &rec, &mu) // runs to completion

	byName := map[string]tool.Tool{}
	for _, tl := range ControlTools(mgr) {
		byName[tl.Name()] = tl
	}
	ctx := context.Background()

	out, err := byName["trigger_run"].Execute(ctx, []byte(`{"input":"do X"}`))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(out, "started run ")
	if id == out || id == "" {
		t.Fatalf("unexpected trigger result %q", out)
	}
	mgr.Wait(id)

	state, err := byName["get_run_state"].Execute(ctx, []byte(`{"run_id":"`+id+`"}`))
	if err != nil || !strings.Contains(state, "completed") {
		t.Errorf("get_run_state = %q (err %v), want status completed", state, err)
	}
	if runs, _ := byName["list_runs"].Execute(ctx, []byte(`{}`)); !strings.Contains(runs, id) {
		t.Errorf("list_runs = %q, want it to contain %s", runs, id)
	}
	if ev, _ := byName["get_run_events"].Execute(ctx, []byte(`{"run_id":"`+id+`"}`)); !strings.Contains(ev, "done") {
		t.Errorf("get_run_events = %q, want a done event", ev)
	}
}

func TestControlTools_checkpointsRewindFork(t *testing.T) {
	var mu sync.Mutex
	var rec []string
	mgr, _ := managerFixture(t, nil, &rec, &mu) // runs to completion

	byName := map[string]tool.Tool{}
	for _, tl := range ControlTools(mgr) {
		byName[tl.Name()] = tl
	}
	ctx := context.Background()

	id, _ := mgr.Trigger("tt", "x")
	mgr.Wait(id)

	cps, err := byName["list_checkpoints"].Execute(ctx, []byte(`{"run_id":"tt"}`))
	if err != nil || !strings.Contains(cps, "seq=") {
		t.Fatalf("list_checkpoints = %q (err %v)", cps, err)
	}

	mu.Lock()
	before := len(rec)
	mu.Unlock()
	if _, err := byName["rewind_run"].Execute(ctx, []byte(`{"run_id":"tt","seq":1,"note":"redo"}`)); err != nil {
		t.Fatal(err)
	}
	mgr.Wait("tt")
	mu.Lock()
	after := len(rec)
	mu.Unlock()
	if after <= before {
		t.Errorf("rewind should re-execute stages: before=%d after=%d", before, after)
	}

	out, err := byName["fork_run"].Execute(ctx, []byte(`{"run_id":"tt","seq":1}`))
	if err != nil || !strings.Contains(out, "→ ") {
		t.Fatalf("fork_run = %q (err %v)", out, err)
	}
	newID := out[strings.LastIndex(out, "→ ")+len("→ "):]
	mgr.Wait(newID)
	if st, err := mgr.State(ctx, newID); err != nil || st.Status != StatusCompleted {
		t.Errorf("forked run %q state = %v (err %v), want completed", newID, st.Status, err)
	}
}

func TestControlTools_steerAndCancel(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var rec []string
	mgr, store := managerFixture(t, gate, &rec, &mu)

	byName := map[string]tool.Tool{}
	for _, tl := range ControlTools(mgr) {
		byName[tl.Name()] = tl
	}
	ctx := context.Background()

	id, _ := mgr.Trigger("steer-1", "x") // blocks in "block"
	if _, err := byName["steer_run"].Execute(ctx, []byte(`{"run_id":"steer-1","note":"be careful"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := byName["cancel_run"].Execute(ctx, []byte(`{"run_id":"steer-1","reason":"stop"}`)); err != nil {
		t.Fatal(err)
	}
	close(gate)
	mgr.Wait(id)

	st, _ := store.Load(ctx, "steer-1")
	if st.Status != StatusCanceled {
		t.Errorf("status = %v, want canceled", st.Status)
	}
}
