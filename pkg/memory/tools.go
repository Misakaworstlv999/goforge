package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// MemoryTools exposes a Store as agent-callable tools scoped to namespace:
// memory_search (deliberate recall) and memory_add (deliberate remember). They
// complement the automatic Source injection — an agent can decide to store a
// durable fact or look one up, and the memory persists across runs.
func MemoryTools(store *Store, namespace string) []tool.Tool {
	return []tool.Tool{
		tool.NewTool("memory_search",
			"Search long-term memory for facts/notes relevant to a query. Returns matching memories, most relevant first.",
			func(ctx context.Context, a struct {
				Query string `json:"query" jsonschema:"description=What to recall,required"`
				K     int    `json:"k" jsonschema:"description=Max results (default 5)"`
			}) (string, error) {
				scored, err := store.Retrieve(ctx, namespace, a.Query, a.K)
				if err != nil {
					return "", err
				}
				if len(scored) == 0 {
					return "(no relevant memory)", nil
				}
				var b strings.Builder
				for _, s := range scored {
					fmt.Fprintf(&b, "- %s\n", s.Document.Text)
				}
				return strings.TrimRight(b.String(), "\n"), nil
			}),

		tool.NewTool("memory_add",
			"Save a fact/note to long-term memory so it can be recalled in future runs.",
			func(ctx context.Context, a struct {
				Text string `json:"text" jsonschema:"description=The fact or note to remember,required"`
			}) (string, error) {
				if strings.TrimSpace(a.Text) == "" {
					return "", fmt.Errorf("memory_add: text is empty")
				}
				id, err := store.Add(ctx, namespace, a.Text, nil)
				if err != nil {
					return "", err
				}
				return "remembered (" + id + ")", nil
			}),
	}
}
