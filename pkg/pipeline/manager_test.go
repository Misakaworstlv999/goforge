package pipeline

import (
	"context"
	"sync"
	"testing"
)

// managerFixture builds a Manager whose runs share a MemoryStore and record
// which stages executed. gate, if non-nil, blocks the "block" stage until closed
// (lets a test position a run at a safe point deterministically).
func managerFixture(t *testing.T, gate <-chan struct{}, rec *[]string, mu *sync.Mutex) (*Manager, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	factory := func(runID string) *Pipeline {
		p := New(StageDeps{State: NewState()}, WithStore(store))
		must(t, AddStage(p, Stage[string, string]{
			Name: "block",
			Run: func(_ context.Context, in string, _ StageDeps) (string, error) {
				if gate != nil {
					<-gate
				}
				mu.Lock()
				*rec = append(*rec, "block")
				mu.Unlock()
				return in, nil
			},
		}))
		must(t, AddStage(p, recStage("after", rec, mu)))
		return p
	}
	return NewManager(factory), store
}

func TestManager_triggerObserveComplete(t *testing.T) {
	var mu sync.Mutex
	var rec []string
	mgr, store := managerFixture(t, nil, &rec, &mu) // no gate: runs to completion

	id, err := mgr.Trigger("", "x")
	if err != nil {
		t.Fatal(err)
	}
	replay, live, cancel, err := mgr.Subscribe(id)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	mgr.Wait(id)

	// Drain replay + remaining live into one slice.
	events := append([]Event(nil), replay...)
	for ev := range live {
		events = append(events, ev)
	}
	if !sawType(events, EventDone) {
		t.Errorf("expected a Done event; got %d events", len(events))
	}
	st, err := store.Load(context.Background(), id)
	if err != nil || st.Status != StatusCompleted {
		t.Errorf("state = %v (err %v), want completed", st.Status, err)
	}
	if got := mgr.List(); len(got) != 1 || got[0] != id {
		t.Errorf("List = %v, want [%s]", got, id)
	}
}

func TestManager_cancelAtSafePoint(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var rec []string
	mgr, store := managerFixture(t, gate, &rec, &mu)

	id, _ := mgr.Trigger("", "x") // run blocks inside "block" stage
	if err := mgr.Cancel(id, "stop"); err != nil {
		t.Fatal(err)
	}
	close(gate) // "block" returns → next safe point applies the queued cancel
	mgr.Wait(id)

	mu.Lock()
	defer mu.Unlock()
	for _, s := range rec {
		if s == "after" {
			t.Error("'after' must not run once canceled")
		}
	}
	st, _ := store.Load(context.Background(), id)
	if st.Status != StatusCanceled {
		t.Errorf("status = %v, want canceled", st.Status)
	}
}

func TestManager_pauseResume(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var rec []string
	mgr, _ := managerFixture(t, gate, &rec, &mu)

	id, _ := mgr.Trigger("", "x")
	replay, live, cancel, _ := mgr.Subscribe(id)
	defer cancel()

	// Both queued before releasing the stage: at the post-"block" safe point the
	// run pauses then immediately resumes (both buffered).
	must(t, mgr.Pause(id))
	must(t, mgr.Resume(id))
	close(gate)
	mgr.Wait(id)

	events := append([]Event(nil), replay...)
	for ev := range live {
		events = append(events, ev)
	}
	if !sawType(events, EventPaused) {
		t.Error("expected a Paused event")
	}
	if !sawType(events, EventDone) {
		t.Error("expected a Done event after resume")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rec) != 2 {
		t.Errorf("both stages should run, ran %v", rec)
	}
}
