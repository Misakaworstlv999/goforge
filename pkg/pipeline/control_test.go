package pipeline

import (
	"context"
	"sync"
	"testing"
)

// recStage records its name when run, so tests can assert which stages executed.
func recStage(name string, rec *[]string, mu *sync.Mutex) Stage[string, string] {
	return strStage(name, func(s string) string {
		mu.Lock()
		*rec = append(*rec, name)
		mu.Unlock()
		return s
	}, nil)
}

func sawType(events []Event, t EventType) bool {
	for _, e := range events {
		if e.Type == t {
			return true
		}
	}
	return false
}

func TestControl_cancelAtSafePoint(t *testing.T) {
	store := NewMemoryStore()
	p := New(StageDeps{State: NewState()}, WithStore(store))
	var mu sync.Mutex
	var rec []string
	must(t, AddStage(p, recStage("a", &rec, &mu)))
	must(t, AddStage(p, recStage("b", &rec, &mu)))

	control := make(chan Control, 1)
	control <- Control{Op: OpCancel, Note: "stop now"}

	events, _ := collect(p.runControlled(context.Background(), "r1", "x", control))
	if lastType(events) != EventCanceled {
		t.Fatalf("want terminal Canceled, got %v", lastType(events))
	}
	mu.Lock()
	ran := len(rec)
	mu.Unlock()
	if ran != 0 {
		t.Errorf("cancel at first safe point should run no stage, ran %v", rec)
	}
	st, err := store.Load(context.Background(), "r1")
	if err != nil || st.Status != StatusCanceled {
		t.Errorf("persisted status = %v (err %v), want canceled", st.Status, err)
	}
}

func TestControl_redirectToStage(t *testing.T) {
	p := New(StageDeps{State: NewState()})
	var mu sync.Mutex
	var rec []string
	must(t, AddStage(p, recStage("a", &rec, &mu)))
	must(t, AddStage(p, recStage("b", &rec, &mu)))
	must(t, AddStage(p, recStage("c", &rec, &mu)))

	control := make(chan Control, 1)
	control <- Control{Op: OpRedirect, Stage: "c"} // jump straight to c

	events, _ := collect(p.runControlled(context.Background(), "r", "x", control))
	if lastType(events) != EventDone {
		t.Fatalf("want Done, got %v", lastType(events))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rec) != 1 || rec[0] != "c" {
		t.Errorf("redirect should run only c, ran %v", rec)
	}
}

func TestControl_steerInjectsBlackboard(t *testing.T) {
	p := New(StageDeps{State: NewState()})
	var seen any
	must(t, AddStage(p, Stage[string, string]{
		Name: "read",
		Run: func(_ context.Context, in string, d StageDeps) (string, error) {
			seen, _ = d.State.Get(SteerKey)
			return in, nil
		},
	}))

	control := make(chan Control, 1)
	control <- Control{Op: OpSteer, Note: "focus on edge cases"}

	if _, err := collect(p.runControlled(context.Background(), "r", "x", control)); err != nil {
		t.Fatal(err)
	}
	acc, ok := seen.([]any)
	if !ok || len(acc) != 1 || acc[0] != "focus on edge cases" {
		t.Errorf("steer note not on blackboard: %#v", seen)
	}
}

func TestControl_pauseThenResume(t *testing.T) {
	store := NewMemoryStore()
	p := New(StageDeps{State: NewState()}, WithStore(store))
	var mu sync.Mutex
	var rec []string
	must(t, AddStage(p, recStage("a", &rec, &mu)))
	must(t, AddStage(p, recStage("b", &rec, &mu)))

	control := make(chan Control, 1)
	control <- Control{Op: OpPause} // applied at the first safe point → blocks
	go func() { control <- Control{Op: OpResume} }()

	events, err := collect(p.runControlled(context.Background(), "r", "x", control))
	if err != nil {
		t.Fatal(err)
	}
	if !sawType(events, EventPaused) {
		t.Error("expected a Paused event from the controller pause")
	}
	if lastType(events) != EventDone {
		t.Fatalf("want Done after resume, got %v", lastType(events))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rec) != 2 {
		t.Errorf("both stages should run after resume, ran %v", rec)
	}
}
