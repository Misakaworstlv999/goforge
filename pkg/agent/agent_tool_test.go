package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

func TestAsTool_delegatesAndReturnsFinal(t *testing.T) {
	sub := New(&mockLLM{responses: []llm.Response{{
		Message:    llm.AssistantMessage("the researched answer"),
		StopReason: llm.StopReasonEnd,
	}}}, tool.NewRegistry())

	tl := AsTool("researcher", "Look something up.", sub)
	if tl.Name() != "researcher" {
		t.Fatalf("name = %q", tl.Name())
	}
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"task":"find X"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "the researched answer" {
		t.Errorf("subagent result = %q, want the child's final response", out)
	}
}

func TestAsTool_propagatesError(t *testing.T) {
	sub := New(&mockLLM{err: errors.New("boom")}, tool.NewRegistry())
	tl := AsTool("x", "x", sub)
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"task":"t"}`)); err == nil {
		t.Error("expected the child's error to propagate")
	}
}

func TestAsTool_depthGuard(t *testing.T) {
	sub := New(&mockLLM{}, tool.NewRegistry())
	tl := AsTool("x", "x", sub)
	// Simulate being nested at the limit already.
	ctx := context.WithValue(context.Background(), depthKey{}, maxSubagentDepth)
	_, err := tl.Execute(ctx, json.RawMessage(`{"task":"t"}`))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Errorf("expected depth-limit error, got %v", err)
	}
}
