package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

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
