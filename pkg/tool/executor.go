package tool

import (
	"context"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// ExecuteAll runs each tool call sequentially and collects results.
// A single tool failure does not affect others — the error is captured in ToolResult.IsError.
func ExecuteAll(ctx context.Context, reg *Registry, calls []llm.ToolCall) []llm.ToolResult {
	results := make([]llm.ToolResult, 0, len(calls))
	for _, call := range calls {
		result, _ := reg.Execute(ctx, call)
		results = append(results, result)
	}
	return results
}
