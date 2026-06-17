package tool

import "context"

// ToolSet is a dynamic, possibly remote-backed collection of tools. Unlike a
// single Tool (known at compile time), a ToolSet is resolved at runtime — e.g.
// tools discovered from an MCP server. Resolving may do I/O, hence the context
// and error.
type ToolSet interface {
	Tools(ctx context.Context) ([]Tool, error)
}

// RegisterSet resolves a ToolSet and registers every tool it yields, so dynamic
// and statically-registered tools share one execution path (Registry.Execute,
// ExecuteParallel, the agent loop). It is atomic only per-tool: a duplicate name
// returns an error and leaves previously-registered tools from the set in place.
func (r *Registry) RegisterSet(ctx context.Context, set ToolSet) error {
	tools, err := set.Tools(ctx)
	if err != nil {
		return err
	}
	return r.Register(tools...)
}
