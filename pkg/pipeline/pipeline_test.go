package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// strStage builds a string→string stage applying fn, with an optional gate.
func strStage(name string, fn func(string) string, gate Gate) Stage[string, string] {
	return Stage[string, string]{
		Name: name,
		Run:  func(_ context.Context, in string, _ StageDeps) (string, error) { return fn(in), nil },
		Gate: gate,
	}
}

func TestPipeline_linear(t *testing.T) {
	p := New(StageDeps{})
	must(t, AddStage(p, strStage("a", func(s string) string { return s + "A" }, nil)))
	must(t, AddStage(p, strStage("b", func(s string) string { return s + "B" }, nil)))

	events, err := collect(p.Run(context.Background(), "p1", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if lastType(events) != EventDone {
		t.Fatalf("want Done, got %v", lastType(events))
	}
	// final stage output flows through: x → xA → xAB
	var lastOutput string
	for _, e := range events {
		if e.Type == EventStageOutput {
			lastOutput = e.Detail
		}
	}
	if lastOutput != "xAB" {
		t.Errorf("hand-off wrong, last output = %q", lastOutput)
	}
}

func TestPipeline_conditionalBranch(t *testing.T) {
	p := New(StageDeps{})
	must(t, AddStage(p, strStage("classify", func(s string) string { return s }, nil)))
	must(t, AddStage(p, strStage("short", func(s string) string { return "SHORT:" + s }, nil)))
	must(t, AddStage(p, strStage("long", func(s string) string { return "LONG:" + s }, nil)))

	p.Route("classify", func(_ context.Context, out any, _ *State) (Route, error) {
		if len(out.(string)) < 4 {
			return Route{Next: "short"}, nil
		}
		return Route{Next: "long"}, nil
	})
	// terminal branches
	p.Route("short", func(context.Context, any, *State) (Route, error) { return Route{Done: true}, nil })
	p.Route("long", func(context.Context, any, *State) (Route, error) { return Route{Done: true}, nil })

	for _, tc := range []struct{ in, want string }{{"hi", "SHORT:hi"}, {"hello", "LONG:hello"}} {
		events, err := collect(p.Run(context.Background(), "p", tc.in))
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, e := range events {
			if e.Type == EventStageOutput {
				got = e.Detail
			}
		}
		if got != tc.want {
			t.Errorf("in %q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestPipeline_cycleBackEdge demonstrates a review stage routing back to coding
// (a cycle), terminated by shared-blackboard state after two rounds.
func TestPipeline_cycleBackEdge(t *testing.T) {
	p := New(StageDeps{})
	must(t, AddStage(p, strStage("code", func(s string) string { return s + "|coded" }, nil)))
	must(t, AddStage(p, strStage("review", func(s string) string { return s + "|reviewed" }, nil)))

	p.Route("review", func(_ context.Context, _ any, st *State) (Route, error) {
		rounds := 0
		if v, ok := st.Get("rounds"); ok {
			rounds = v.(int)
		}
		rounds++
		st.Set("rounds", rounds)
		if rounds >= 2 {
			return Route{Done: true}, nil // approved after 2 rounds
		}
		return Route{Next: "code"}, nil // bounce back to coding
	})

	events, err := collect(p.Run(context.Background(), "p", "spec"))
	if err != nil {
		t.Fatal(err)
	}
	if lastType(events) != EventDone {
		t.Fatalf("want Done, got %v", lastType(events))
	}
	// code stage should have run twice (entered the cycle once).
	codeEnters := 0
	for _, e := range events {
		if e.Type == EventStageEnter && e.Stage == "code" {
			codeEnters++
		}
	}
	if codeEnters != 2 {
		t.Errorf("expected code to run twice (back-edge), got %d", codeEnters)
	}
	if v, _ := p.State().Get("rounds"); v != 2 {
		t.Errorf("rounds = %v, want 2", v)
	}
}

func TestPipeline_gateFailRetryExhausted(t *testing.T) {
	attempts := 0
	failing := strStage("flaky",
		func(s string) string { attempts++; return s },
		AutoGate(func(context.Context, any) error { return errors.New("always fails") }),
	)
	p := New(StageDeps{}, WithMaxRetries(2))
	must(t, AddStage(p, failing))

	events, err := collect(p.Run(context.Background(), "p", "x"))
	if !errors.Is(err, ErrStageRetriesExceeded) {
		t.Fatalf("want ErrStageRetriesExceeded, got %v", err)
	}
	if lastType(events) != EventFailed {
		t.Errorf("want Failed event, got %v", lastType(events))
	}
	// initial run + 2 retries = 3 executions
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (1 + maxRetries)", attempts)
	}
}

func TestPipeline_unknownStageRoute(t *testing.T) {
	p := New(StageDeps{})
	must(t, AddStage(p, strStage("only", func(s string) string { return s }, nil)))
	p.Route("only", func(context.Context, any, *State) (Route, error) { return Route{Next: "ghost"}, nil })

	_, err := collect(p.Run(context.Background(), "p", "x"))
	if !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("want ErrUnknownStage, got %v", err)
	}
}

func TestPipeline_maxStepsGuard(t *testing.T) {
	p := New(StageDeps{}, WithMaxSteps(5))
	must(t, AddStage(p, strStage("loop", func(s string) string { return s }, nil)))
	// route always back to itself: an infinite passing cycle.
	p.Route("loop", func(context.Context, any, *State) (Route, error) { return Route{Next: "loop"}, nil })

	_, err := collect(p.Run(context.Background(), "p", "x"))
	if !errors.Is(err, ErrMaxStepsExceeded) {
		t.Fatalf("want ErrMaxStepsExceeded, got %v", err)
	}
}

func TestPipeline_noStages(t *testing.T) {
	_, err := collect(New(StageDeps{}).Run(context.Background(), "p", nil))
	if !errors.Is(err, ErrNoStages) {
		t.Fatalf("want ErrNoStages, got %v", err)
	}
}

// TestPipeline_blackboardHandoff shows a stage writing shared state another reads.
func TestPipeline_blackboardHandoff(t *testing.T) {
	p := New(StageDeps{})
	must(t, AddStage(p, Stage[string, string]{
		Name: "writer",
		Run: func(_ context.Context, in string, deps StageDeps) (string, error) {
			deps.State.Set("shared", strings.ToUpper(in))
			return in, nil
		},
	}))
	must(t, AddStage(p, Stage[string, string]{
		Name: "reader",
		Run: func(_ context.Context, _ string, deps StageDeps) (string, error) {
			v, _ := deps.State.Get("shared")
			return fmt.Sprintf("read:%v", v), nil
		},
	}))

	events, err := collect(p.Run(context.Background(), "p", "abc"))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, e := range events {
		if e.Type == EventStageOutput && e.Stage == "reader" {
			got = e.Detail
		}
	}
	if got != "read:ABC" {
		t.Errorf("blackboard hand-off failed: %q", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
