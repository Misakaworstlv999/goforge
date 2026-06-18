package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestState_setGetReducer(t *testing.T) {
	s := NewState()

	t.Run("default replace", func(t *testing.T) {
		s.Set("k", "a")
		s.Set("k", "b")
		if v, _ := s.Get("k"); v != "b" {
			t.Errorf("got %v, want b", v)
		}
	})

	t.Run("append reducer accumulates", func(t *testing.T) {
		s.SetReducer("log", AppendReducer)
		s.Set("log", "one")
		s.Set("log", "two")
		v, _ := s.Get("log")
		got, ok := v.([]any)
		if !ok || len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Errorf("got %#v", v)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		if _, ok := s.Get("absent"); ok {
			t.Error("expected absent")
		}
	})
}

func TestState_tempSnapshot(t *testing.T) {
	s := NewState()
	s.Set("keep", 1)
	s.Set(TempPrefix+"scratch", 2)

	snap := s.Snapshot()
	if _, ok := snap[TempPrefix+"scratch"]; ok {
		t.Error("temp key must be excluded from snapshot")
	}
	if snap["keep"] != 1 {
		t.Error("persistable key missing from snapshot")
	}
	// temp key still live in-process.
	if v, ok := s.Get(TempPrefix + "scratch"); !ok || v != 2 {
		t.Error("temp key should remain Get-able")
	}
}

func TestStateSource(t *testing.T) {
	s := NewState()
	s.Set("title", "Widget")
	s.Set("count", 3)

	t.Run("selected keys rendered in order", func(t *testing.T) {
		msgs, err := StateSource(s, "title", "count")(context.Background(), "task")
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("got %d messages", len(msgs))
		}
		body := msgs[0].Content
		if !strings.Contains(body, "title: Widget") || !strings.Contains(body, "count: 3") {
			t.Errorf("missing rendered keys: %q", body)
		}
	})

	t.Run("empty when no keys present", func(t *testing.T) {
		msgs, err := StateSource(s, "absent")(context.Background(), "task")
		if err != nil || msgs != nil {
			t.Errorf("want nil msgs, got %v err %v", msgs, err)
		}
	})
}
