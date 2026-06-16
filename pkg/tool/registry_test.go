package tool

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func echoTool(name string) Tool {
	return NewTool[struct {
		Msg string `json:"msg" jsonschema:"required"`
	}](name, "Echo "+name, func(_ context.Context, args struct {
		Msg string `json:"msg" jsonschema:"required"`
	}) (string, error) {
		return args.Msg, nil
	})
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(echoTool("a"), echoTool("b")); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if tool, ok := reg.Get("a"); !ok {
		t.Error("Get('a') not found")
	} else if tool.Name() != "a" {
		t.Errorf("Get('a').Name() = %q", tool.Name())
	}

	if _, ok := reg.Get("nonexistent"); ok {
		t.Error("Get('nonexistent') should return false")
	}
}

func TestRegistry_DuplicateName(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(echoTool("dup")); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	err := reg.Register(echoTool("dup"))
	if err == nil {
		t.Fatal("Register() expected duplicate error, got nil")
	}
}

func TestRegistry_SchemasSorted(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(echoTool("charlie"), echoTool("alpha"), echoTool("bravo"))

	schemas := reg.Schemas()
	if len(schemas) != 3 {
		t.Fatalf("Schemas() len = %d, want 3", len(schemas))
	}

	expected := []string{"alpha", "bravo", "charlie"}
	for i, s := range schemas {
		if s.Name != expected[i] {
			t.Errorf("Schemas()[%d].Name = %q, want %q", i, s.Name, expected[i])
		}
	}
}

func TestRegistry_ExecuteSuccess(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(echoTool("echo"))

	result, err := reg.Execute(context.Background(), llm.ToolCall{
		ID:   "call-1",
		Name: "echo",
		Args: `{"msg":"hello"}`,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("result.Content = %q, want %q", result.Content, "hello")
	}
	if result.CallID != "call-1" {
		t.Errorf("result.CallID = %q, want %q", result.CallID, "call-1")
	}
	if result.IsError {
		t.Error("result.IsError should be false")
	}
}

func TestRegistry_ExecuteNotFound(t *testing.T) {
	reg := NewRegistry()

	result, err := reg.Execute(context.Background(), llm.ToolCall{
		ID:   "call-1",
		Name: "missing",
		Args: "{}",
	})
	if err == nil {
		t.Fatal("Execute() expected error for missing tool, got nil")
	}
	if !result.IsError {
		t.Error("result.IsError should be true")
	}
}

func TestRegistry_ExecuteToolError(t *testing.T) {
	reg := NewRegistry()
	failTool := NewTool[struct{}]("fail", "Fails", func(_ context.Context, _ struct{}) (string, error) {
		return "", fmt.Errorf("intentional failure")
	})
	_ = reg.Register(failTool)

	result, err := reg.Execute(context.Background(), llm.ToolCall{
		ID:   "call-2",
		Name: "fail",
		Args: "{}",
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	if !result.IsError {
		t.Error("result.IsError should be true")
	}
	if result.CallID != "call-2" {
		t.Errorf("result.CallID = %q, want %q", result.CallID, "call-2")
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewRegistry()
	for i := 0; i < 10; i++ {
		name := string(rune('a' + i))
		_ = reg.Register(echoTool(name))
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.Schemas()
			_, _ = reg.Get("a")
			_, _ = reg.Execute(context.Background(), llm.ToolCall{
				ID: "c", Name: "a", Args: `{"msg":"x"}`,
			})
		}()
	}
	wg.Wait()
}
