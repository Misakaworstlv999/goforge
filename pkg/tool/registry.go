package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// Registry is a concurrent-safe collection of tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds one or more tools. Returns an error if a name is already registered.
func (r *Registry) Register(tools ...Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range tools {
		name := t.Name()
		if _, exists := r.tools[name]; exists {
			return fmt.Errorf("tool %q already registered", name)
		}
		r.tools[name] = t
	}
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// Schemas returns sorted tool schemas for passing to an LLM.
// Sorted by name to ensure prompt cache stability.
func (r *Registry) Schemas() []llm.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	schemas := make([]llm.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		schemas = append(schemas, t.Schema())
	}
	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].Name < schemas[j].Name
	})
	return schemas
}

// Execute finds a tool by the call's name and runs it.
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	t, ok := r.Get(call.Name)
	if !ok {
		return llm.ToolResult{
			CallID:  call.ID,
			Content: fmt.Sprintf("tool %q not found", call.Name),
			IsError: true,
		}, fmt.Errorf("tool %q not found", call.Name)
	}

	result, err := t.Execute(ctx, json.RawMessage(call.Args))
	if err != nil {
		return llm.ToolResult{
			CallID:  call.ID,
			Content: err.Error(),
			IsError: true,
		}, err
	}

	return llm.ToolResult{
		CallID:  call.ID,
		Content: result,
	}, nil
}
