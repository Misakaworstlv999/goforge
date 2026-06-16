package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClock_DefaultUTC(t *testing.T) {
	clock := NewClock()

	result, err := clock.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed, err := time.Parse(time.RFC3339, result)
	if err != nil {
		t.Fatalf("result %q is not RFC3339: %v", result, err)
	}
	if parsed.Location().String() != "UTC" {
		t.Errorf("expected UTC, got %s", parsed.Location())
	}
}

func TestClock_WithTimezone(t *testing.T) {
	clock := NewClock()

	args, _ := json.Marshal(ClockArgs{Timezone: "Asia/Shanghai"})
	result, err := clock.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "+08:00") {
		t.Errorf("result %q should contain +08:00 offset", result)
	}
}

func TestClock_InvalidTimezone(t *testing.T) {
	clock := NewClock()

	args, _ := json.Marshal(ClockArgs{Timezone: "Invalid/Nowhere"})
	_, err := clock.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestClock_Schema(t *testing.T) {
	clock := NewClock()

	if clock.Name() != "current_time" {
		t.Errorf("Name() = %q, want %q", clock.Name(), "current_time")
	}
}
