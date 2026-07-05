package pipeline

import (
	"context"
	"sync"
	"testing"
)

// TestManager_onCompleteFires verifies the general post-run hook: it fires once
// per completed run, with the run id, before Wait returns (so a caller can rely
// on the hook having run — e.g. memory extraction).
func TestManager_onCompleteFires(t *testing.T) {
	store := NewMemoryStore()
	factory := func(string) *Pipeline {
		p := New(StageDeps{State: NewState()}, WithStore(store))
		_ = AddStage(p, Stage[string, string]{
			Name: "echo",
			Run:  func(_ context.Context, in string, _ StageDeps) (string, error) { return in, nil },
		})
		return p
	}

	var mu sync.Mutex
	var completed []string
	mgr := NewManager(factory, WithOnComplete(func(id string) {
		mu.Lock()
		completed = append(completed, id)
		mu.Unlock()
	}))
	defer mgr.Close()

	id, err := mgr.Trigger("r", "x")
	if err != nil {
		t.Fatal(err)
	}
	mgr.Wait(id)

	mu.Lock()
	defer mu.Unlock()
	if len(completed) != 1 || completed[0] != "r" {
		t.Errorf("onComplete fired %v, want [r]", completed)
	}
}

// TestManager_nilOnCompleteIsSafe confirms the zero-value hook (no option) does
// not fire or panic.
func TestManager_nilOnCompleteIsSafe(t *testing.T) {
	store := NewMemoryStore()
	mgr := NewManager(func(string) *Pipeline {
		p := New(StageDeps{State: NewState()}, WithStore(store))
		_ = AddStage(p, Stage[string, string]{
			Name: "echo",
			Run:  func(_ context.Context, in string, _ StageDeps) (string, error) { return in, nil },
		})
		return p
	})
	defer mgr.Close()
	id, _ := mgr.Trigger("r", "x")
	mgr.Wait(id) // must not panic
}
