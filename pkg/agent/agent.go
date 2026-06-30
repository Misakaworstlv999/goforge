package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/Misakaworstlv999/goforge/internal/telemetry"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ErrMaxStepsExceeded is reported (as an EventError) when the ReAct loop reaches
// its step limit without the LLM producing a final, tool-free response.
var ErrMaxStepsExceeded = errors.New("agent: max steps exceeded")

const defaultMaxSteps = 10

// Agent is the Ring 3 boundary interface. It runs a task to completion, emitting
// observations as a pull-based iterator: callers range over it and receive each
// Event (or a terminal error) as the loop progresses.
type Agent interface {
	Run(ctx context.Context, task string) iter.Seq2[Event, error]
}

// SimpleAgent is the concrete ReAct implementation: think (LLM) → act (tools) →
// observe (results) → repeat, until the LLM stops requesting tools or maxSteps
// is reached. It is intentionally a hand-written loop with no graph abstraction.
type SimpleAgent struct {
	llm         llm.LLM
	registry    *tool.Registry
	system      string
	model       string
	maxSteps    int
	toolTimeout time.Duration
	policy      ContextPolicy
	transcript  func(llm.Message)
	interrupt   Interrupt
}

// Interrupt is a cooperative control seam called at each ReAct step boundary (a
// safe point, before every model call). It lets an outer controller pause (by
// blocking), steer (by returning messages to inject into the conversation), or
// abort the run (by returning a non-nil error) at step granularity rather than
// only between pipeline stages. nil ⇒ no checks (default behavior unchanged).
// The agent is Ring 3 and knows nothing of Ring 4: the pipeline supplies an
// adapter over its run-control channel (see pipeline.RunAgent).
type Interrupt func(ctx context.Context, step int) (inject []llm.Message, err error)

// Option configures a SimpleAgent.
type Option func(*SimpleAgent)

// WithSystemPrompt sets the system prompt prepended to the conversation.
func WithSystemPrompt(prompt string) Option {
	return func(a *SimpleAgent) { a.system = prompt }
}

// WithModel sets the model name passed on each LLM call.
func WithModel(model string) Option {
	return func(a *SimpleAgent) { a.model = model }
}

// WithMaxSteps caps the number of think-act cycles. Non-positive values are ignored.
func WithMaxSteps(n int) Option {
	return func(a *SimpleAgent) {
		if n > 0 {
			a.maxSteps = n
		}
	}
}

// WithToolTimeout sets a per-tool execution deadline. Zero means no timeout.
func WithToolTimeout(d time.Duration) Option {
	return func(a *SimpleAgent) { a.toolTimeout = d }
}

// WithTranscriptSink registers a sink that receives every message as it is added
// to the live conversation — the seeded system/source/task messages, each
// assistant response, and each tool result — in order, as produced. It is the
// durable-log seam (M5-003): the sink captures the FULL, lossless transcript,
// while in-loop compaction only ever reduces what is SENT to the model (the
// compacted projection is never sent to the sink). nil ⇒ no transcript capture.
func WithTranscriptSink(sink func(llm.Message)) Option {
	return func(a *SimpleAgent) { a.transcript = sink }
}

// WithInterrupt installs a cooperative control seam checked at each ReAct step
// boundary (pause/steer/abort). nil ⇒ no checks.
func WithInterrupt(i Interrupt) Option {
	return func(a *SimpleAgent) { a.interrupt = i }
}

// WithContextPolicy enables context engineering: source injection before the
// task and budget-triggered compaction during the loop. The zero policy (the
// default) disables both, preserving baseline behavior.
func WithContextPolicy(p ContextPolicy) Option {
	return func(a *SimpleAgent) { a.policy = p }
}

// New constructs a SimpleAgent over the given LLM client and tool registry.
func New(client llm.LLM, registry *tool.Registry, opts ...Option) *SimpleAgent {
	a := &SimpleAgent{
		llm:      client,
		registry: registry,
		maxSteps: defaultMaxSteps,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// compile-time assertion that SimpleAgent satisfies the Agent boundary.
var _ Agent = (*SimpleAgent)(nil)

// Run executes the ReAct loop for task and returns an iterator of events. The
// iterator yields Think/ToolCall/ToolResult events as the loop progresses and
// terminates with exactly one Response (success) or Error (failure) event. If
// the consumer stops early (breaks the range), the loop returns promptly.
func (a *SimpleAgent) Run(ctx context.Context, task string) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		counter := a.policy.Counter
		if counter == nil {
			counter = NewEstimator()
		}
		compact := a.policy.Compact
		if compact == nil {
			retain := a.policy.RetainRecent
			if retain <= 0 {
				retain = defaultRetainRecent
			}
			compact = func(ctx context.Context, c llm.LLM, m []llm.Message, b int) ([]llm.Message, error) {
				return compactMessages(ctx, c, m, b, retain)
			}
		}

		messages := make([]llm.Message, 0, 4)
		// record appends a message to the live conversation and forwards it to the
		// transcript sink (the durable-log seam). Compaction reassigns messages
		// directly, never through record, so the lossless original stays in the
		// sink while the sent context is reduced.
		record := func(m llm.Message) {
			messages = append(messages, m)
			if a.transcript != nil {
				a.transcript(m)
			}
		}

		if a.system != "" {
			record(llm.SystemMessage(a.system))
		}
		// Inject context sources ahead of the task (the long-term-memory seam).
		// Strict: a failing source aborts the run — silently dropping retrieved
		// context would let the agent act confidently on missing information.
		for _, src := range a.policy.Sources {
			msgs, err := src(ctx, task)
			if err != nil {
				err = fmt.Errorf("loading context source: %w", err)
				yield(ErrorEvent(err, 0), err)
				return
			}
			for _, m := range msgs {
				record(m)
			}
		}
		record(llm.UserMessage(task))

		chatOpts := a.chatOpts()
		lastUsage := 0 // provider-reported token count from the latest Chat

		for step := 0; step < a.maxSteps; step++ {
			// Cooperative safe point: let an outer controller pause/steer/abort
			// between reasoning steps (much finer-grained than per-stage).
			if a.interrupt != nil {
				inject, err := a.interrupt(ctx, step)
				if err != nil {
					yield(ErrorEvent(err, step), err)
					return
				}
				for _, m := range inject {
					record(m)
				}
			}

			// LLM span (no-op unless telemetry.Init wired a provider). Nests under
			// the pipeline stage span when the agent runs inside a pipeline.
			llmCtx, llmSpan := telemetry.StartLLM(ctx, a.model, step)
			resp, err := a.llm.Chat(llmCtx, messages, chatOpts...)
			if err != nil {
				telemetry.End(llmSpan, err)
				yield(ErrorEvent(err, step), err)
				return
			}
			telemetry.RecordLLMUsage(llmSpan, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
			telemetry.End(llmSpan, nil)
			lastUsage = resp.Usage.TotalTokens
			record(resp.Message)

			// Surface any reasoning text the model produced this step.
			if resp.Message.Content != "" {
				if !yield(ThinkEvent(resp.Message.Content, &resp.Usage, step), nil) {
					return
				}
			}

			// Terminal: no tool calls means this is the final answer.
			if resp.StopReason != llm.StopReasonToolCall || len(resp.Message.ToolCalls) == 0 {
				yield(ResponseEvent(resp.Message.Content, &resp.Usage, step), nil)
				return
			}

			// Act: announce each requested tool call.
			for _, tc := range resp.Message.ToolCalls {
				if !yield(ToolCallEvent(tc, step), nil) {
					return
				}
			}

			// Execute concurrently; one failure does not cancel the others. Tool
			// span (no-op unless telemetry is wired) covers the whole batch.
			toolCtx, toolSpan := telemetry.StartTool(ctx, step, len(resp.Message.ToolCalls), toolCallNames(resp.Message.ToolCalls))
			results := tool.ExecuteParallel(toolCtx, a.registry, resp.Message.ToolCalls, a.toolTimeout)
			telemetry.End(toolSpan, nil)

			// Observe: feed every result back into the conversation.
			for _, r := range results {
				if !yield(ToolResultEvent(r, step), nil) {
					return
				}
				record(llm.ToolMessage(r.CallID, r.Content, r.IsError))
			}

			// Budget check: prefer the provider's real token count, fall back to
			// the estimator. Compact when over budget (or message-count limit).
			if a.policy.MaxTokens > 0 {
				used := lastUsage
				if used == 0 {
					used = counter.Count(messages)
				}
				over := used > a.policy.MaxTokens ||
					(a.policy.MaxMessages > 0 && len(messages) > a.policy.MaxMessages)
				if over {
					compacted, err := compact(ctx, a.llm, messages, a.policy.MaxTokens)
					if err != nil {
						yield(ErrorEvent(err, step), err)
						return
					}
					messages = compacted
					lastUsage = 0 // real count is stale after compaction; re-derive next turn
				}
			}
		}

		yield(ErrorEvent(ErrMaxStepsExceeded, a.maxSteps), ErrMaxStepsExceeded)
	}
}

// toolCallNames joins the requested tool-call names for a telemetry span
// attribute (comma-separated, order preserved).
func toolCallNames(calls []llm.ToolCall) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Name
	}
	return strings.Join(names, ",")
}

// chatOpts builds the per-call LLM options: registered tool schemas plus an
// optional model override.
func (a *SimpleAgent) chatOpts() []llm.Option {
	opts := []llm.Option{llm.WithTools(a.registry.Schemas()...)}
	if a.model != "" {
		opts = append(opts, llm.WithModel(a.model))
	}
	return opts
}
