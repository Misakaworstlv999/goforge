package tool

import (
	"context"
	"fmt"
	"testing"

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
