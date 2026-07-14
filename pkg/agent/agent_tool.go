package agent

import (
	"context"
	"fmt"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// maxSubagentDepth bounds agent-as-tool nesting (a subagent may itself hold
// subagent tools). Exceeding it fails the call rather than recursing forever —
// the delegation analogue of the ReAct/pipeline step guards.
const maxSubagentDepth = 5

type depthKey struct{}

// AsTool wraps a child Agent as a Tool so a parent agent can delegate a subtask
// to it mid-loop, like any other tool call ("agent as tool"). The child runs to
// completion on the given task and ONLY its final response returns to the parent
// — the child's own reasoning/tool turns stay in the child's conversation, never
// touching the parent's. This is a context firewall (the parent isn't polluted
// by the child's intermediate steps) and, because a single tool_call is answered
// by a single tool result, it keeps the parent's message list format-valid.
//
// The child gets a FRESH, isolated conversation (its own system prompt + this
// task); it does not inherit the parent's history. Scope the child's tools
// (tool.Filter / a registry Subset) and context (ContextPolicy) when you build
// it, e.g. a read-only "researcher" subagent.
//
// Execution is synchronous: the parent blocks until the child finishes. (An
// async/timeout variant that detaches long children and signals completion back
// is planned separately.)
func AsTool(name, desc string, sub Agent) tool.Tool {
	return tool.NewTool(name, desc, func(ctx context.Context, a struct {
		Task string `json:"task" jsonschema:"description=The self-contained subtask for this agent to carry out,required"`
	}) (string, error) {
		depth, _ := ctx.Value(depthKey{}).(int)
		if depth >= maxSubagentDepth {
			return "", fmt.Errorf("agent: subagent depth limit %d exceeded", maxSubagentDepth)
		}
		childCtx := context.WithValue(ctx, depthKey{}, depth+1)

		var final string
		for ev, err := range sub.Run(childCtx, a.Task) {
			if err != nil {
				return "", err
			}
			if ev.Type == EventResponse {
				final = ev.Content
			}
		}
		if final == "" {
			return "(subagent produced no final response)", nil
		}
		return final, nil
	})
}
