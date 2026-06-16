package tool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestExecuteAll_AllSuccess(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(NewTool[struct {
		X int `json:"x"`
	}]("double", "Doubles X", func(_ context.Context, args struct {
		X int `json:"x"`
	}) (string, error) {
		return fmt.Sprintf("%d", args.X*2), nil
	}))

	calls := []llm.ToolCall{
		{ID: "c1", Name: "double", Args: `{"x":3}`},
		{ID: "c2", Name: "double", Args: `{"x":5}`},
	}

	results := ExecuteAll(context.Background(), reg, calls)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	if results[0].Content != "6" || results[0].CallID != "c1" || results[0].IsError {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1].Content != "10" || results[1].CallID != "c2" || results[1].IsError {
		t.Errorf("results[1] = %+v", results[1])
	}
}

func TestExecuteAll_PartialFailure(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(echoTool("ok"))

	calls := []llm.ToolCall{
		{ID: "c1", Name: "ok", Args: `{"msg":"hi"}`},
		{ID: "c2", Name: "missing", Args: `{}`},
		{ID: "c3", Name: "ok", Args: `{"msg":"bye"}`},
	}

	results := ExecuteAll(context.Background(), reg, calls)
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}

	if results[0].IsError {
		t.Error("results[0] should succeed")
	}
	if !results[1].IsError {
		t.Error("results[1] should be error (missing tool)")
	}
	if results[2].IsError {
		t.Error("results[2] should succeed despite earlier failure")
	}
}

func TestExecuteAll_Empty(t *testing.T) {
	reg := NewRegistry()
	results := ExecuteAll(context.Background(), reg, nil)
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

type emptyArgs struct{}

// newSleepTool returns a tool that blocks for d, but respects context
// cancellation so timeouts surface as an error.
func newSleepTool(name string, d time.Duration) Tool {
	return NewTool[emptyArgs](name, "sleeps", func(ctx context.Context, _ emptyArgs) (string, error) {
		select {
		case <-time.After(d):
			return name + ":done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
}

func newOKTool(name string) Tool {
	return NewTool[emptyArgs](name, "ok", func(_ context.Context, _ emptyArgs) (string, error) {
		return name + ":ok", nil
	})
}

func newErrTool(name string) Tool {
	return NewTool[emptyArgs](name, "fails", func(_ context.Context, _ emptyArgs) (string, error) {
		return "", errors.New(name + " failed")
	})
}

func callsFor(names ...string) []llm.ToolCall {
	calls := make([]llm.ToolCall, len(names))
	for i, n := range names {
		calls[i] = llm.ToolCall{ID: n, Name: n, Args: "{}"}
	}
	return calls
}

func TestExecuteParallel_preservesOrder(t *testing.T) {
	reg := NewRegistry()
	// Register so completion order (c,b,a) differs from input order (a,b,c):
	// "a" sleeps longest, "c" shortest. Results must still match input order.
	if err := reg.Register(
		newSleepTool("a", 60*time.Millisecond),
		newSleepTool("b", 30*time.Millisecond),
		newSleepTool("c", 5*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}

	calls := callsFor("a", "b", "c")
	results := ExecuteParallel(context.Background(), reg, calls, 0)

	want := []string{"a:done", "b:done", "c:done"}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	for i, w := range want {
		if results[i].CallID != calls[i].ID {
			t.Errorf("results[%d].CallID = %q, order not preserved", i, results[i].CallID)
		}
		if results[i].Content != w || results[i].IsError {
			t.Errorf("results[%d] = %+v, want content %q", i, results[i], w)
		}
	}
}

func TestExecuteParallel_oneFailureDoesNotAffectOthers(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(newOKTool("ok1"), newErrTool("bad"), newOKTool("ok2")); err != nil {
		t.Fatal(err)
	}

	results := ExecuteParallel(context.Background(), reg, callsFor("ok1", "bad", "ok2"), 0)

	if results[0].IsError || results[0].Content != "ok1:ok" {
		t.Errorf("ok1 should succeed: %+v", results[0])
	}
	if !results[1].IsError {
		t.Errorf("bad should be marked IsError: %+v", results[1])
	}
	if results[2].IsError || results[2].Content != "ok2:ok" {
		t.Errorf("ok2 should succeed: %+v", results[2])
	}
}

func TestExecuteParallel_perToolTimeout(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(newSleepTool("slow", 500*time.Millisecond), newOKTool("fast")); err != nil {
		t.Fatal(err)
	}

	results := ExecuteParallel(context.Background(), reg, callsFor("slow", "fast"), 20*time.Millisecond)

	if !results[0].IsError {
		t.Errorf("slow tool should time out and be IsError: %+v", results[0])
	}
	if results[1].IsError || results[1].Content != "fast:ok" {
		t.Errorf("fast tool should succeed despite sibling timeout: %+v", results[1])
	}
}

func TestExecuteParallel_empty(t *testing.T) {
	reg := NewRegistry()
	results := ExecuteParallel(context.Background(), reg, nil, 0)
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}
