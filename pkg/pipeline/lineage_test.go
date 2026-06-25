package pipeline

import (
	"context"
	"path/filepath"
	"testing"
)

// TestLineage_bothStores verifies checkpoint lineage (P3): a completed run leaves
// a seq-ordered trail of checkpoints, and any earlier one can be loaded (the
// basis for rewind/fork). Exercised against both store implementations.
func TestLineage_bothStores(t *testing.T) {
	stores := map[string]func(*testing.T) CheckpointStore{
		"memory": func(*testing.T) CheckpointStore { return NewMemoryStore() },
		"sqlite": func(t *testing.T) CheckpointStore {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "lin.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
	for name, mk := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := mk(t)
			p := New(StageDeps{State: NewState()}, WithStore(store))
			must(t, AddStage(p, strStage("a", func(s string) string { return s + "A" }, nil)))
			must(t, AddStage(p, strStage("b", func(s string) string { return s + "B" }, nil)))

			if _, err := collect(p.Run(ctx, "r", "x")); err != nil {
				t.Fatal(err)
			}

			cps, err := store.ListCheckpoints(ctx, "r")
			if err != nil {
				t.Fatal(err)
			}
			if len(cps) < 2 {
				t.Fatalf("expected a multi-step lineage, got %d checkpoints", len(cps))
			}
			// Seqs are monotonic.
			for i := 1; i < len(cps); i++ {
				if cps[i].Seq <= cps[i-1].Seq {
					t.Errorf("seqs not monotonic: %d then %d", cps[i-1].Seq, cps[i].Seq)
				}
			}
			// Any earlier checkpoint is loadable (time-travel basis).
			at, err := store.LoadAt(ctx, "r", cps[0].Seq)
			if err != nil || at.Seq != cps[0].Seq || at.PipelineID != "r" {
				t.Errorf("LoadAt(%d) = %+v (err %v)", cps[0].Seq, at, err)
			}
			if _, err := store.LoadAt(ctx, "r", 99999); err != ErrNotFound {
				t.Errorf("LoadAt(missing) = %v, want ErrNotFound", err)
			}
		})
	}
}
