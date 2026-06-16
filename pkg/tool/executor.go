package tool

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

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

// ExecuteParallel runs all tool calls concurrently and returns results in the
// same order as calls. A non-positive timeout means no per-tool deadline;
// otherwise each tool runs under its own context.WithTimeout.
//
// One tool failing (or timing out) does not cancel the others: every error is
// encoded into its ToolResult (IsError=true) so the LLM can observe and react,
// which is why the errgroup callbacks always return nil.
func ExecuteParallel(ctx context.Context, reg *Registry, calls []llm.ToolCall, timeout time.Duration) []llm.ToolResult {
	results := make([]llm.ToolResult, len(calls))

	g, ctx := errgroup.WithContext(ctx)
	for i, call := range calls {
		g.Go(func() error {
			callCtx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			// reg.Execute already encodes failures into the returned ToolResult,
			// so the error value is intentionally ignored here.
			results[i], _ = reg.Execute(callCtx, call)
			return nil
		})
	}
	_ = g.Wait()

	return results
}
