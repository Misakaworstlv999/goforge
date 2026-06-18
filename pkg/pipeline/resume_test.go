package pipeline

import (
	"context"
	"errors"
	"testing"
)

// approvalPipeline: a draft stage gated by HumanGate, then a publish stage. Built
// fresh each time to simulate resuming in a new process against a shared store.
func approvalPipeline(store CheckpointStore) *Pipeline {
	p := New(StageDeps{}, WithStore(store))
	_ = AddStage(p, Stage[string, string]{
		Name: "draft",
		Run:  func(_ context.Context, in string, _ StageDeps) (string, error) { return "draft:" + in, nil },
		Gate: HumanGate(),
	})
	_ = AddStage(p, strStage("publish", func(s string) string { return "published:" + s }, nil))
	return p
}

func TestInterruptResume_approved(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	events, err := collect(approvalPipeline(store).Run(ctx, "p1", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if lastType(events) != EventPaused {
		t.Fatalf("want Paused, got %v", lastType(events))
	}
	st, _ := store.Load(ctx, "p1")
	if st.Status != StatusPaused || st.CurrentStage != "draft" {
		t.Fatalf("bad paused state: %+v", st)
	}

	// resume on a fresh instance (process-restart simulation)
	events, err = collect(approvalPipeline(store).Resume(ctx, "p1", Decision{Approved: true}))
	if err != nil {
		t.Fatal(err)
	}
	if lastType(events) != EventDone {
		t.Fatalf("want Done, got %v", lastType(events))
	}
	var got string
	for _, e := range events {
		if e.Type == EventStageOutput {
			got = e.Detail
		}
	}
	if got != "published:draft:hello" {
		t.Errorf("resume hand-off wrong: %q", got)
	}
}

func TestInterruptResume_rejected(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := collect(approvalPipeline(store).Run(ctx, "p2", "x")); err != nil {
		t.Fatal(err)
	}
	// reject → draft retried → pauses again at the human gate
	events, err := collect(approvalPipeline(store).Resume(ctx, "p2", Decision{Approved: false, Reason: "needs work"}))
	if err != nil {
		t.Fatal(err)
	}
	if lastType(events) != EventPaused {
		t.Fatalf("want Paused again after rejection, got %v", lastType(events))
	}
	sawRetry := false
	for _, e := range events {
		if e.Type == EventRetry {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Error("expected a retry event after rejection")
	}
}

func TestResume_errors(t *testing.T) {
	ctx := context.Background()

	t.Run("no store", func(t *testing.T) {
		p := New(StageDeps{})
		must(t, AddStage(p, strStage("a", func(s string) string { return s }, nil)))
		if _, err := collect(p.Resume(ctx, "x", Decision{})); !errors.Is(err, ErrNoStore) {
			t.Errorf("want ErrNoStore, got %v", err)
		}
	})

	t.Run("not paused", func(t *testing.T) {
		store := NewMemoryStore()
		p := New(StageDeps{}, WithStore(store))
		must(t, AddStage(p, strStage("a", func(s string) string { return s }, nil)))
		if _, err := collect(p.Run(ctx, "done", "x")); err != nil {
			t.Fatal(err)
		}
		if _, err := collect(p.Resume(ctx, "done", Decision{Approved: true})); !errors.Is(err, ErrNotPaused) {
			t.Errorf("want ErrNotPaused, got %v", err)
		}
	})
}
