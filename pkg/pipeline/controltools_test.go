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
