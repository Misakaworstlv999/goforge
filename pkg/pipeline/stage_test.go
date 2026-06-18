package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
)

func TestStage_compile_typeAssertion(t *testing.T) {
	s := Stage[string, string]{
		Name: "up",
		Run: func(_ context.Context, in string, _ StageDeps) (string, error) {
			return strings.ToUpper(in), nil
		},
	}
	n, err := s.compile()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("correct type runs", func(t *testing.T) {
		out, err := n.run(context.Background(), "hi", StageDeps{})
		if err != nil || out.(string) != "HI" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})

	t.Run("nil input becomes zero value", func(t *testing.T) {
		out, err := n.run(context.Background(), nil, StageDeps{})
		if err != nil || out.(string) != "" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})

	t.Run("wrong type errors at boundary", func(t *testing.T) {
		if _, err := n.run(context.Background(), 42, StageDeps{}); err == nil {
			t.Fatal("expected type-mismatch error")
		}
	})
}

func TestStage_compile_validation(t *testing.T) {
	if _, err := (Stage[int, int]{Run: func(context.Context, int, StageDeps) (int, error) { return 0, nil }}).compile(); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := (Stage[int, int]{Name: "x"}).compile(); err == nil {
		t.Error("expected error for nil Run")
	}
}

func TestGates(t *testing.T) {
	ctx := context.Background()

	t.Run("AutoGate pass/fail", func(t *testing.T) {
		pass := AutoGate(func(context.Context, any) error { return nil })
		if r, _ := pass(ctx, "x", StageDeps{}); r.Status != GatePass {
			t.Error("want pass")
		}
		fail := AutoGate(func(context.Context, any) error { return errors.New("nope") })
		if r, _ := fail(ctx, "x", StageDeps{}); r.Status != GateFail || r.Reason != "nope" {
			t.Errorf("want fail with reason, got %+v", r)
		}
	})

	t.Run("HumanGate awaits", func(t *testing.T) {
		if r, _ := HumanGate()(ctx, "x", StageDeps{}); r.Status != GateAwaitHuman {
			t.Error("want await human")
		}
	})

	t.Run("LLMReviewGate", func(t *testing.T) {
		pass := LLMReviewGate(&scriptLLM{replies: []string{"PASS"}}, "be good")
		if r, _ := pass(ctx, "x", StageDeps{}); r.Status != GatePass {
			t.Error("want pass")
		}
		fail := LLMReviewGate(&scriptLLM{replies: []string{"FAIL: too short"}}, "be good")
		if r, _ := fail(ctx, "x", StageDeps{}); r.Status != GateFail {
			t.Error("want fail")
		}
	})
}

func TestRunAgent(t *testing.T) {
	out, err := RunAgent(context.Background(),
		StageDeps{LLM: &scriptLLM{replies: []string{"the answer"}}},
		"question?", "you are helpful", agent.ContextPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer" {
		t.Errorf("got %q", out)
	}
}
