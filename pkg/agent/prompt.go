package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

const defaultRole = "You are a helpful assistant that can use tools when needed."

// BuildSystemPrompt assembles a system prompt from a role definition, the
// available tools, and the context policy's breadth/depth hints. It is a pure,
// deterministic function (stable tool ordering) so the prompt is prompt-cache
// friendly. The result can be passed to WithSystemPrompt.
func BuildSystemPrompt(role string, tools []llm.ToolSchema, policy ContextPolicy) string {
	var b strings.Builder

	if strings.TrimSpace(role) == "" {
		role = defaultRole
	}
	b.WriteString(role)

	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		desc := make(map[string]string, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
			desc[t.Name] = t.Description
		}
		sort.Strings(names) // deterministic order for cache stability
		b.WriteString("\n\nAvailable tools:")
		for _, n := range names {
			fmt.Fprintf(&b, "\n- %s: %s", n, desc[n])
		}
	}

	if hint := strategyHint(policy.Breadth, policy.Depth); hint != "" {
		b.WriteString("\n\n")
		b.WriteString(hint)
	}

	return b.String()
}

// strategyHint maps breadth/depth into a natural-language directive. Returns ""
// for the zero policy (wide+shallow) so an unconfigured policy adds nothing.
func strategyHint(br Breadth, d Depth) string {
	if br == BreadthWide && d == DepthShallow {
		return ""
	}
	return fmt.Sprintf("Context strategy: gather %s and reason %s.", breadthWord(br), depthWord(d))
}

func breadthWord(b Breadth) string {
	switch b {
	case BreadthNarrow:
		return "a narrow, focused set of sources"
	case BreadthMedium:
		return "a moderate set of sources"
	default:
		return "broadly across many sources"
	}
}

func depthWord(d Depth) string {
	switch d {
	case DepthDeep:
		return "deeply into detail"
	case DepthMedium:
		return "at a moderate level of detail"
	default:
		return "at a high level"
	}
}
