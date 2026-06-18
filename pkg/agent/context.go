package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// Breadth and Depth describe a stage's context strategy (consumed by Ring 4 in
// M5; defined here so the policy is self-contained). They are advisory hints
// surfaced into the system prompt, not hard constraints.
type Breadth int

const (
	BreadthWide Breadth = iota
	BreadthNarrow
	BreadthMedium
)

type Depth int

const (
	DepthShallow Depth = iota
	DepthDeep
	DepthMedium
)

// ContextSource loads extra context messages for a task, injected ahead of the
// user task. It is a function type (lightweight strategy) and is the pluggable
// seam for retrieval-augmented context — including future long-term memory /
// vector retrieval (see the M8 milestone). A trivial implementation is
// StaticSource.
type ContextSource func(ctx context.Context, task string) ([]llm.Message, error)

// CompactFunc shrinks a message history to fit within a token budget. nil in a
// ContextPolicy selects DefaultCompact.
type CompactFunc func(ctx context.Context, client llm.LLM, messages []llm.Message, budget int) ([]llm.Message, error)

// ContextPolicy configures short-term context management for an agent run. The
// zero value disables everything (no sources, no compaction) — preserving the
// default agent behavior.
type ContextPolicy struct {
	MaxTokens    int             // 0 ⇒ no compaction
	MaxMessages  int             // optional message-count trigger (0 ⇒ ignored)
	RetainRecent int             // recent messages never compacted (0 ⇒ default)
	Sources      []ContextSource // loaded and prepended before the task
	Breadth      Breadth
	Depth        Depth
	Compact      CompactFunc  // nil ⇒ DefaultCompact
	Counter      TokenCounter // nil ⇒ Estimator (used as fallback budget signal)
}

const (
	defaultRetainRecent = 4
	defaultTruncRunes   = 500
)

// StaticSource returns a ContextSource that always injects the given messages.
// Useful for tests and for fixed preamble context; real retrieval sources
// (vector/memory) arrive in M8.
func StaticSource(msgs ...llm.Message) ContextSource {
	return func(context.Context, string) ([]llm.Message, error) {
		return msgs, nil
	}
}

// DefaultCompact is the standard three-tier, middle-out CompactFunc. It is
// called only when the history is over budget, and reduces the middle region
// (everything between the head — system + first user task — and the most recent
// RetainRecent messages) in escalating tiers until within budget or exhausted.
//
// Compaction is round-atomic: it never leaves an assistant tool_call without its
// tool results or vice versa, which the OpenAI/Anthropic APIs reject.
func DefaultCompact(ctx context.Context, client llm.LLM, messages []llm.Message, budget int) ([]llm.Message, error) {
	return compactMessages(ctx, client, messages, budget, defaultRetainRecent)
}

// compactMessages is the retain-parameterized implementation behind DefaultCompact.
// It reduces only what is sent to the model; the lossless original is preserved
// out-of-band by the agent's transcript sink (see WithTranscriptSink).
func compactMessages(ctx context.Context, client llm.LLM, messages []llm.Message, budget, retain int) ([]llm.Message, error) {
	if budget <= 0 || len(messages) == 0 {
		return messages, nil
	}
	counter := NewEstimator()
	head, middle, tail := splitRegions(messages, retain)
	if len(middle) == 0 {
		// Only head + tail remain; nothing left to compact. Best-effort return.
		return messages, nil
	}

	// Tier 1: drop tool I/O (bulky args/results) from oldest rounds, keeping
	// assistant reasoning text. Round-atomic: stripping an assistant's ToolCalls
	// and removing its tool-result messages together keeps pairing valid.
	middle = dropToolIO(head, middle, tail, budget, counter)
	if counter.Count(rejoin(head, middle, tail)) <= budget {
		return rejoin(head, middle, tail), nil
	}

	// Tier 2: truncate any remaining long content.
	middle = truncateLongContent(middle, defaultTruncRunes)
	if counter.Count(rejoin(head, middle, tail)) <= budget {
		return rejoin(head, middle, tail), nil
	}

	// Tier 3: summarize the whole middle into one message.
	summary, err := summarizeConversation(ctx, client, middle)
	if err != nil {
		return nil, err
	}
	// Best-effort: return even if still over budget (never loop forever).
	return rejoin(head, []llm.Message{summary}, tail), nil
}

// splitRegions partitions messages into head (system + up to the first user
// message), middle (compactible), and tail (the last `retain` messages, snapped
// backward off any leading tool-result so the tail stays pairing-valid).
func splitRegions(messages []llm.Message, retain int) (head, middle, tail []llm.Message) {
	if retain <= 0 {
		retain = defaultRetainRecent
	}

	headEnd := 0
	for i, m := range messages {
		if m.Role == llm.RoleUser {
			headEnd = i + 1
			break
		}
	}
	// No user message: head is the leading run of system messages.
	if headEnd == 0 {
		for _, m := range messages {
			if m.Role != llm.RoleSystem {
				break
			}
			headEnd++
		}
	}

	tailStart := max(len(messages)-retain, headEnd)
	// A tail must not start on a tool result (its owning assistant would be in
	// the middle). Snap backward to include the owning assistant.
	for tailStart > headEnd && messages[tailStart].Role == llm.RoleTool {
		tailStart--
	}

	return messages[:headEnd], messages[headEnd:tailStart], messages[tailStart:]
}

// dropToolIO removes tool I/O from the middle, oldest round first, stopping as
// soon as the budget is met. For each assistant message with tool calls it
// clears the ToolCalls and removes the immediately following tool-result
// messages, keeping the assistant's text. Operates on a copy.
func dropToolIO(head, middle, tail []llm.Message, budget int, counter TokenCounter) []llm.Message {
	m := cloneMessages(middle)
	for i := 0; i < len(m); i++ {
		if m[i].Role != llm.RoleAssistant || len(m[i].ToolCalls) == 0 {
			continue
		}
		m[i].ToolCalls = nil
		j := i + 1
		for j < len(m) && m[j].Role == llm.RoleTool {
			j++
		}
		m = append(m[:i+1], m[j:]...)
		if counter.Count(rejoin(head, m, tail)) <= budget {
			break
		}
	}
	return m
}

// truncateLongContent shortens any message Content longer than maxRunes,
// appending a notice. Pairing is untouched (only content strings change).
func truncateLongContent(middle []llm.Message, maxRunes int) []llm.Message {
	m := cloneMessages(middle)
	for i := range m {
		r := []rune(m[i].Content)
		if len(r) > maxRunes {
			m[i].Content = string(r[:maxRunes]) + fmt.Sprintf("\n... [truncated, %d runes]", len(r))
		}
	}
	return m
}

// summarizeConversation asks the LLM to condense the middle region into a single
// assistant message. The summary carries no tool calls, so it is pairing-safe.
func summarizeConversation(ctx context.Context, client llm.LLM, middle []llm.Message) (llm.Message, error) {
	prompt := []llm.Message{
		llm.SystemMessage("Summarize the following conversation excerpt concisely, preserving key facts, decisions, and tool results. Output plain text only."),
		llm.UserMessage(renderMessages(middle)),
	}
	resp, err := client.Chat(ctx, prompt)
	if err != nil {
		return llm.Message{}, fmt.Errorf("summarizing context: %w", err)
	}
	content := resp.Message.Content
	if content == "" {
		content = "[summary unavailable]"
	}
	return llm.AssistantMessage("[summary of earlier conversation] " + content), nil
}

// renderMessages flattens messages into a plain-text transcript for summarization.
func renderMessages(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, " [tool_call %s(%s)]", tc.Name, tc.Args)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func cloneMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	copy(out, in)
	return out
}

// rejoin concatenates the three regions into a fresh slice.
func rejoin(head, middle, tail []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(head)+len(middle)+len(tail))
	out = append(out, head...)
	out = append(out, middle...)
	out = append(out, tail...)
	return out
}
