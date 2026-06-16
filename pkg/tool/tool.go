package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// Tool is the core abstraction for an executable tool that an LLM can invoke.
type Tool interface {
	Name() string
	Description() string
	Schema() llm.ToolSchema
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// NewTool creates a Tool from a typed function using generics.
// JSON Schema is generated automatically via reflection on Args.
func NewTool[Args any](name, desc string, fn func(context.Context, Args) (string, error)) Tool {
	r := &jsonschema.Reflector{DoNotReference: true}
	schema := r.Reflect(new(Args))

	// LLM tool schemas should be a plain object definition, not a full document.
	// Strip the top-level $schema and $id that the reflector adds.
	schema.Version = ""
	schema.ID = ""

	return &funcTool[Args]{
		name: name,
		desc: desc,
		fn:   fn,
		schema: llm.ToolSchema{
			Name:        name,
			Description: desc,
			Parameters:  schema,
		},
	}
}

type funcTool[Args any] struct {
	name   string
	desc   string
	fn     func(context.Context, Args) (string, error)
	schema llm.ToolSchema
}

func (t *funcTool[Args]) Name() string           { return t.name }
func (t *funcTool[Args]) Description() string    { return t.desc }
func (t *funcTool[Args]) Schema() llm.ToolSchema { return t.schema }

func (t *funcTool[Args]) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args Args
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("tool %q: unmarshal args: %w", t.name, err)
	}
	return t.fn(ctx, args)
}
