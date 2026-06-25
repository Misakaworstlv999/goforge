package pipeline

import (
	"context"
	"sync"
	"testing"
)

// TestManager_rewindAndFork proves time-travel: after a run completes, rewinding
// to an earlier checkpoint re-executes its stages (same id), and forking starts
// an independent run from that checkpoint.
func TestManager_rewindAndFork(t *testing.T) {
	var mu sync.Mutex
	var rec []string
	mgr, store := managerFixture(t, nil, &rec, &mu) // no gate ⇒ runs to completion

	id, err := mgr.Trigger("r", "x")
	if err != nil {
		t.Fatal(err)
	}
	mgr.Wait(id)
	mu.Lock()
	first := len(rec)
	mu.Unlock()
	if first == 0 {
		t.Fatal("run did not execute any stages")
	}

	cps, err := store.ListCheckpoints(context.Background(), id)
	if err != nil || len(cps) == 0 {
		t.Fatalf("no lineage: %d (err %v)", len(cps), err)
	}

	// Rewind re-runs from the earliest checkpoint → stages execute again.
	if err := mgr.Rewind(id, cps[0].Seq, "redo with care"); err != nil {
		t.Fatal(err)
	}
	mgr.Wait(id)
	mu.Lock()
	afterRewind := len(rec)
	mu.Unlock()
	if afterRewind <= first {
		t.Errorf("rewind should re-execute stages: before=%d after=%d", first, afterRewind)
	}

	// Fork starts an independent run from the same checkpoint.
	forkID, err := mgr.Fork(id, "", cps[0].Seq)
	if err != nil {
		t.Fatal(err)
	}
	if forkID == id || forkID == "" {
		t.Fatalf("fork id = %q, want a fresh id", forkID)
	}
	mgr.Wait(forkID)
	if st, err := store.Load(context.Background(), forkID); err != nil || st.Status != StatusCompleted {
		t.Errorf("forked run state = %v (err %v), want completed", st.Status, err)
	}
}
