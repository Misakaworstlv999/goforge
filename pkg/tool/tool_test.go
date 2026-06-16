package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/invopop/jsonschema"
)

type addArgs struct {
	A float64 `json:"a" jsonschema:"description=First number,required"`
	B float64 `json:"b" jsonschema:"description=Second number,required"`
}

type nestedArgs struct {
	Query  string     `json:"query" jsonschema:"description=Search query,required"`
	Filter filterSpec `json:"filter" jsonschema:"description=Optional filter"`
}

type filterSpec struct {
	Limit int    `json:"limit,omitempty" jsonschema:"description=Max results"`
	Tag   string `json:"tag,omitempty" jsonschema:"description=Tag filter"`
}

func addFn(_ context.Context, args addArgs) (string, error) {
	b, err := json.Marshal(args.A + args.B)
	return string(b), err
}

func TestNewTool_Interface(t *testing.T) {
	tool := NewTool("add", "Add two numbers", addFn)

	if tool.Name() != "add" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "add")
	}
	if tool.Description() != "Add two numbers" {
		t.Errorf("Description() = %q, want %q", tool.Description(), "Add two numbers")
	}

	schema := tool.Schema()
	if schema.Name != "add" {
		t.Errorf("Schema().Name = %q, want %q", schema.Name, "add")
	}
	if schema.Description != "Add two numbers" {
		t.Errorf("Schema().Description = %q, want %q", schema.Description, "Add two numbers")
	}
}

func TestNewTool_SchemaGeneration(t *testing.T) {
	tool := NewTool("add", "Add two numbers", addFn)

	s, ok := tool.Schema().Parameters.(*jsonschema.Schema)
	if !ok {
		t.Fatalf("Schema().Parameters is %T, want *jsonschema.Schema", tool.Schema().Parameters)
	}

	if s.Type != "object" {
		t.Errorf("schema type = %q, want %q", s.Type, "object")
	}

	if s.Properties == nil {
		t.Fatal("schema properties is nil")
	}

	aSchema, ok := s.Properties.Get("a")
	if !ok {
		t.Fatal("missing property 'a'")
	}
	if aSchema.Type != "number" {
		t.Errorf("property 'a' type = %q, want %q", aSchema.Type, "number")
	}
	if aSchema.Description != "First number" {
		t.Errorf("property 'a' description = %q, want %q", aSchema.Description, "First number")
	}

	bSchema, ok := s.Properties.Get("b")
	if !ok {
		t.Fatal("missing property 'b'")
	}
	if bSchema.Type != "number" {
		t.Errorf("property 'b' type = %q, want %q", bSchema.Type, "number")
	}

	requiresA := false
	requiresB := false
	for _, r := range s.Required {
		if r == "a" {
			requiresA = true
		}
		if r == "b" {
			requiresB = true
		}
	}
	if !requiresA {
		t.Error("property 'a' should be required")
	}
	if !requiresB {
		t.Error("property 'b' should be required")
	}
}

func TestNewTool_NestedSchema(t *testing.T) {
	tool := NewTool("search", "Search things", func(_ context.Context, args nestedArgs) (string, error) {
		return args.Query, nil
	})

	s, ok := tool.Schema().Parameters.(*jsonschema.Schema)
	if !ok {
		t.Fatalf("Parameters type = %T, want *jsonschema.Schema", tool.Schema().Parameters)
	}

	filterProp, ok := s.Properties.Get("filter")
	if !ok {
		t.Fatal("missing property 'filter'")
	}
	if filterProp.Type != "object" {
		t.Errorf("filter type = %q, want %q", filterProp.Type, "object")
	}
	if filterProp.Properties == nil {
		t.Fatal("filter properties is nil")
	}
	limitProp, ok := filterProp.Properties.Get("limit")
	if !ok {
		t.Fatal("missing filter.limit property")
	}
	if limitProp.Type != "integer" {
		t.Errorf("filter.limit type = %q, want %q", limitProp.Type, "integer")
	}
}

func TestNewTool_ExecuteSuccess(t *testing.T) {
	tool := NewTool("add", "Add", addFn)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"a":3,"b":4}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != "7" {
		t.Errorf("Execute() = %q, want %q", result, "7")
	}
}

func TestNewTool_ExecuteInvalidJSON(t *testing.T) {
	tool := NewTool("add", "Add", addFn)

	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("Execute() expected error for invalid JSON, got nil")
	}
}

func TestNewTool_ExecuteFnError(t *testing.T) {
	errBoom := errors.New("boom")
	tool := NewTool("fail", "Always fails", func(_ context.Context, _ addArgs) (string, error) {
		return "", errBoom
	})

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"a":1,"b":2}`))
	if !errors.Is(err, errBoom) {
		t.Errorf("Execute() error = %v, want %v", err, errBoom)
	}
}

func TestNewTool_SchemaNoDocumentMeta(t *testing.T) {
	tool := NewTool("test", "Test", addFn)

	s, ok := tool.Schema().Parameters.(*jsonschema.Schema)
	if !ok {
		t.Fatalf("Parameters type = %T, want *jsonschema.Schema", tool.Schema().Parameters)
	}

	if s.Version != "" {
		t.Errorf("schema $schema = %q, want empty", s.Version)
	}
	if s.ID != "" {
		t.Errorf("schema $id = %q, want empty", s.ID)
	}
}
