package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// TestStores_roundtrip exercises both CheckpointStore implementations through
// the same scenario, so MemoryStore and SQLiteStore stay behaviorally aligned.
func TestStores_roundtrip(t *testing.T) {
	stores := map[string]func(t *testing.T) CheckpointStore{
		"memory": func(*testing.T) CheckpointStore { return NewMemoryStore() },
		"sqlite": func(t *testing.T) CheckpointStore {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "ckpt.db"))
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

			st := &PipelineState{
				PipelineID:   "pid",
				CurrentStage: "s1",
				Status:       StatusPaused,
				RetryCount:   map[string]int{"s1": 1},
				Blackboard:   map[string]any{"k": "v", "n": 7},
				StageInput:   "in",
				StageOutput:  "out",
				UpdatedAt:    time.Now(),
			}
			if err := store.Save(ctx, st); err != nil {
				t.Fatal(err)
			}

			got, err := store.Load(ctx, "pid")
			if err != nil {
				t.Fatal(err)
			}
			if got.CurrentStage != "s1" || got.Status != StatusPaused {
				t.Errorf("state mismatch: %+v", got)
			}
			if got.Blackboard["k"] != "v" || got.Blackboard["n"] != 7 {
				t.Errorf("blackboard gob roundtrip mismatch: %+v", got.Blackboard)
			}
			if got.StageInput != "in" || got.StageOutput != "out" || got.RetryCount["s1"] != 1 {
				t.Errorf("io/retry mismatch: %+v", got)
			}

			infos, err := store.List(ctx)
			if err != nil || len(infos) != 1 || infos[0].PipelineID != "pid" {
				t.Errorf("list: %v err %v", infos, err)
			}

			if _, err := store.Load(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Errorf("want ErrNotFound, got %v", err)
			}

			// durable log: two appends accumulate in order.
			must(t, store.AppendHistory(ctx, "pid", []llm.Message{llm.UserMessage("u1"), llm.AssistantMessage("a1")}))
			must(t, store.AppendHistory(ctx, "pid", []llm.Message{llm.UserMessage("u2")}))
			hist, err := store.History(ctx, "pid")
			if err != nil {
				t.Fatal(err)
			}
			if len(hist) != 3 || hist[0].Content != "u1" || hist[2].Content != "u2" {
				t.Errorf("durable log wrong: %+v", hist)
			}

			// audit log: insertion order preserved.
			must(t, store.Audit(ctx, AuditEntry{Timestamp: time.Now(), PipelineID: "pid", Stage: "s1", Action: ActionEnter}))
			must(t, store.Audit(ctx, AuditEntry{Timestamp: time.Now(), PipelineID: "pid", Stage: "s1", Action: ActionComplete}))
			log, err := store.AuditLog(ctx, "pid")
			if err != nil || len(log) != 2 || log[0].Action != ActionEnter || log[1].Action != ActionComplete {
				t.Errorf("audit log wrong: %+v err %v", log, err)
			}
		})
	}
}
