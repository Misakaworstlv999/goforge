package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// A turn handles a single line of user input within a mode's REPL. Each mode
// builds a turn closure that captures its own conversation state.
type turn func(line string)

// chatTurn (M1): plain streaming chat with no tools. It keeps a running message
// history so the conversation is multi-turn.
func chatTurn(ctx context.Context, client llm.LLM, out io.Writer, system string) turn {
	messages := []llm.Message{llm.SystemMessage(system)}
	return func(line string) {
		messages = append(messages, llm.UserMessage(line))

		var full strings.Builder
		for chunk, err := range client.ChatStream(ctx, messages) {
			if err != nil {
				fmt.Fprintf(out, "\nError: %v\n", err)
				return
			}
			if chunk.Delta != "" {
				fmt.Fprint(out, chunk.Delta)
				full.WriteString(chunk.Delta)
			}
		}
		fmt.Fprintln(out)
		messages = append(messages, llm.AssistantMessage(full.String()))
	}
}

// toolsTurn (M2): single-step tool calling — one tool round, then a final
// answer. Demonstrates the tool system without the full ReAct loop.
func toolsTurn(ctx context.Context, client llm.LLM, reg *tool.Registry, out io.Writer, system string) turn {
	messages := []llm.Message{llm.SystemMessage(system)}
	return func(line string) {
		messages = append(messages, llm.UserMessage(line))

		resp, err := client.Chat(ctx, messages, llm.WithTools(reg.Schemas()...))
		if err != nil {
			fmt.Fprintf(out, "Error: %v\n", err)
			return
		}
		messages = append(messages, resp.Message)

		if resp.StopReason != llm.StopReasonToolCall || len(resp.Message.ToolCalls) == 0 {
			fmt.Fprintln(out, resp.Message.Content)
			return
		}

		fmt.Fprintf(out, "[Calling %d tool(s)...]\n", len(resp.Message.ToolCalls))
		for _, tc := range resp.Message.ToolCalls {
			fmt.Fprintf(out, "  → %s(%s)\n", tc.Name, tc.Args)
		}

		results := tool.ExecuteAll(ctx, reg, resp.Message.ToolCalls)
		for _, r := range results {
			renderToolResult(out, r)
			messages = append(messages, llm.ToolMessage(r.CallID, r.Content))
		}

		final, err := client.Chat(ctx, messages, llm.WithTools(reg.Schemas()...))
		if err != nil {
			fmt.Fprintf(out, "Error: %v\n", err)
			return
		}
		fmt.Fprintln(out, final.Message.Content)
		messages = append(messages, final.Message)
	}
}

// agentTurn (M3): each line is a task run to completion through the ReAct agent,
// streaming its event flow.
func agentTurn(ctx context.Context, a agent.Agent, out io.Writer) turn {
	return func(line string) {
		for ev, err := range a.Run(ctx, line) {
			renderAgentEvent(out, ev, err)
		}
	}
}

// renderToolResult prints a single tool result with a success/failure marker.
func renderToolResult(out io.Writer, r llm.ToolResult) {
	marker := "✓"
	if r.IsError {
		marker = "✗"
	}
	fmt.Fprintf(out, "  %s %s: %s\n", marker, r.CallID, r.Content)
}

// renderAgentEvent renders one agent event to out.
func renderAgentEvent(out io.Writer, ev agent.Event, err error) {
	switch ev.Type {
	case agent.EventThink:
		if ev.Content != "" {
			fmt.Fprintf(out, "[think] %s\n", ev.Content)
		}
	case agent.EventToolCall:
		fmt.Fprintf(out, "  → %s(%s)\n", ev.ToolCall.Name, ev.ToolCall.Args)
	case agent.EventToolResult:
		renderToolResult(out, *ev.ToolResult)
	case agent.EventResponse:
		fmt.Fprintf(out, "\n%s\n", ev.Content)
	case agent.EventError:
		fmt.Fprintf(out, "Error: %v\n", err)
	}
}

// banner returns the introductory line(s) for a mode. When a registry is
// provided its tool names are listed so the user sees exactly what's available.
func banner(mode config.Mode, maxSteps int, reg *tool.Registry) string {
	switch mode {
	case config.ModeTools:
		return fmt.Sprintf("GoForge — Tool Calling Mode (type 'exit' to quit)\n"+
			"Available tools: %s\n---", toolNames(reg))
	case config.ModeAgent:
		return fmt.Sprintf("GoForge — ReAct Agent Mode (type 'exit' to quit)\n"+
			"Available tools: %s | max steps: %d\n---", toolNames(reg), maxSteps)
	default:
		return "GoForge — Chat Mode (type 'exit' to quit)\n---"
	}
}

// toolNames returns a comma-separated, sorted list of the registry's tool names.
func toolNames(reg *tool.Registry) string {
	if reg == nil {
		return "(none)"
	}
	schemas := reg.Schemas() // already sorted by name
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
