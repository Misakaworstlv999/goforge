// Package agent implements Ring 3 (Agent Runtime): a hand-written ReAct loop
// that drives an LLM through think-act-observe cycles using registered tools.
package agent

import "github.com/Misakaworstlv999/goforge/pkg/llm"

// EventType discriminates the variants of Event. Together with Event it forms a
// tagged-union (sum type) — the Go-idiomatic alternative to an interface
// hierarchy, keeping the ReAct loop's emissions flat and easy to switch on.
type EventType int

const (
	// EventThink carries the LLM's reasoning text for a step.
	EventThink EventType = iota
	// EventToolCall signals a single tool invocation requested by the LLM.
	EventToolCall
	// EventToolResult carries the outcome of a single tool execution.
	EventToolResult
	// EventResponse carries the agent's final answer.
	EventResponse
	// EventError signals that the loop terminated due to an error.
	EventError
)

// String returns a short human-readable label for logging and CLI output.
func (t EventType) String() string {
	switch t {
	case EventThink:
		return "think"
	case EventToolCall:
		return "tool_call"
	case EventToolResult:
		return "tool_result"
	case EventResponse:
		return "response"
	case EventError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is a single observation emitted by an Agent during its run. The Type
// field selects which optional payload is populated. Content always holds a
// human-readable description; ToolCall/ToolResult/Usage carry typed detail for
// the relevant variants. Step records the 0-based ReAct iteration for debugging.
type Event struct {
	Type       EventType
	Content    string
	ToolCall   *llm.ToolCall   // set when Type == EventToolCall
	ToolResult *llm.ToolResult // set when Type == EventToolResult
	Usage      *llm.Usage      // set on EventThink / EventResponse when available
	Step       int
}

// ThinkEvent reports the LLM's reasoning text produced on a step.
func ThinkEvent(content string, usage *llm.Usage, step int) Event {
	return Event{Type: EventThink, Content: content, Usage: usage, Step: step}
}

// ToolCallEvent reports a single tool invocation the LLM requested.
func ToolCallEvent(call llm.ToolCall, step int) Event {
	return Event{Type: EventToolCall, Content: call.Name, ToolCall: &call, Step: step}
}

// ToolResultEvent reports the outcome of a single tool execution.
func ToolResultEvent(result llm.ToolResult, step int) Event {
	return Event{Type: EventToolResult, Content: result.Content, ToolResult: &result, Step: step}
}

// ResponseEvent reports the agent's final answer.
func ResponseEvent(content string, usage *llm.Usage, step int) Event {
	return Event{Type: EventResponse, Content: content, Usage: usage, Step: step}
}

// ErrorEvent reports a terminal error encountered during the loop. The wrapped
// error is returned alongside this event via the iterator's second value; the
// Content mirrors err.Error() so consumers that only inspect events still see it.
func ErrorEvent(err error, step int) Event {
	return Event{Type: EventError, Content: err.Error(), Step: step}
}
