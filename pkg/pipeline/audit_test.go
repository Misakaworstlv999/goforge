package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestAudit_recordsTransitions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	p := New(StageDeps{}, WithStore(store))
	must(t, AddStage(p, strStage("a", func(s string) string { return s }, nil)))
	must(t, AddStage(p, strStage("b", func(s string) string { return s }, nil)))

	if _, err := collect(p.Run(ctx, "p", "x")); err != nil {
		t.Fatal(err)
	}

	log, err := store.AuditLog(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range log {
		actions = append(actions, e.Action)
	}
	// enter(a) gate_pass(a) enter(b) gate_pass(b) complete(b)
	want := strings.Join([]string{ActionEnter, ActionGatePass, ActionEnter, ActionGatePass, ActionComplete}, ",")
	if strings.Join(actions, ",") != want {
		t.Errorf("audit actions = %v, want %v", actions, want)
	}
}

func TestAudit_recordsRetryAndFail(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	p := New(StageDeps{}, WithStore(store), WithMaxRetries(1))
	must(t, AddStage(p, strStage("flaky", func(s string) string { return s },
		AutoGate(func(context.Context, any) error { return context.Canceled }))))

	collect(p.Run(ctx, "p", "x")) //nolint:errcheck // failure asserted via audit

	log, _ := store.AuditLog(ctx, "p")
	var gotRetry, gotFail bool
	for _, e := range log {
		switch e.Action {
		case ActionRetry:
			gotRetry = true
		case ActionFail:
			gotFail = true
		}
	}
	if !gotRetry || !gotFail {
		t.Errorf("expected retry and fail audit entries; got %+v", log)
	}
}
