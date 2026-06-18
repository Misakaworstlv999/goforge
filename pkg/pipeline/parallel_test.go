package pipeline

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestParallelStage_fanOutJoin(t *testing.T) {
	mk := func(name, suffix string) Stage[string, string] {
		return Stage[string, string]{
			Name: name,
			Run: func(_ context.Context, in string, deps StageDeps) (string, error) {
				// Each worker records into a reducer-guarded key (branch-safe write).
				deps.State.Set("results", in+suffix)
				return in + suffix, nil
			},
		}
	}
	state := NewState()
	state.SetReducer("results", AppendReducer)
	deps := StageDeps{State: state}

	par := ParallelStage("fanout", []Stage[string, string]{
		mk("w1", "-a"), mk("w2", "-b"), mk("w3", "-c"),
	}, func(outs []string) string {
		sort.Strings(outs)
		return strings.Join(outs, ",")
	})

	n, err := par.compile()
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.run(context.Background(), "x", deps)
	if err != nil {
		t.Fatal(err)
	}
	if out.(string) != "x-a,x-b,x-c" {
		t.Errorf("join wrong: %q", out)
	}
	// All three concurrent writes survived via the reducer (order-independent).
	got, _ := state.Get("results")
	if acc, ok := got.([]any); !ok || len(acc) != 3 {
		t.Errorf("expected 3 accumulated results, got %#v", got)
	}
}

func TestParallelStage_oneFailureFailsComposite(t *testing.T) {
	good := Stage[string, string]{Name: "ok", Run: func(_ context.Context, in string, _ StageDeps) (string, error) { return in, nil }}
	bad := Stage[string, string]{Name: "bad", Run: func(context.Context, string, StageDeps) (string, error) {
		return "", errors.New("boom")
	}}
	par := ParallelStage("fanout", []Stage[string, string]{good, bad}, func(o []string) string { return strings.Join(o, ",") })

	n, _ := par.compile()
	if _, err := n.run(context.Background(), "x", StageDeps{State: NewState()}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected composite to fail with sub-stage error, got %v", err)
	}
}
