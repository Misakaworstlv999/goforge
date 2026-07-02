package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// Source adapts a Retriever into an agent context source (the M4-005 seam): it
// retrieves the top-k memories most relevant to the task and renders them as ONE
// read-only system message injected ahead of the task. It returns the bare
// ContextSource function type (func(ctx, task) ([]llm.Message, error)), which is
// assignable to agent.ContextPolicy.Sources — so this package auto-injects memory
// without importing Ring 3.
//
// Best-effort by design: unlike a required knowledge source, long-term memory is
// auxiliary recall, so a retrieval failure (e.g. the embeddings API is down) is
// swallowed — it injects nothing rather than aborting an otherwise-fine agent
// run. (This deliberately differs from the strict "abort on source error"
// contract of mandatory sources.) Returns nil when there is no relevant memory.
func Source(r Retriever, namespace string, k int) func(ctx context.Context, task string) ([]llm.Message, error) {
	return func(ctx context.Context, task string) ([]llm.Message, error) {
		scored, err := r.Retrieve(ctx, namespace, task, k)
		if err != nil || len(scored) == 0 {
			return nil, nil // best-effort: no memory this turn
		}
		var b strings.Builder
		b.WriteString("Relevant long-term memory (read-only; may be stale):\n")
		for _, s := range scored {
			fmt.Fprintf(&b, "- %s\n", s.Document.Text)
		}
		return []llm.Message{llm.SystemMessage(b.String())}, nil
	}
}
