// Package pipeline implements Ring 4 (Pipeline Engine): a cyclic, conditionally
// routed finite-state machine that orchestrates agent stages, separated by
// verification gates, with checkpointing and human-in-the-loop interrupt/resume.
//
// Two inter-stage data channels (mirroring Eino / tRPC-Agent-Go / ADK-Go):
//   - horizontal typed hand-off: a Stage[In,Out]'s output becomes the next
//     stage's input;
//   - a shared blackboard: the named-key State all stages read (via the M4
//     ContextSource seam) and write (via stage output).
//
// The FSM is itself a directed graph with cycles: a RouteFunc may route to any
// registered stage, including an earlier one (retry, review-bounce). The engine
// is deliberately sequential — one stage active at a time — which keeps the
// checkpoint/resume/audit model linear and deterministic. Stage-level
// concurrency (fan-out/fan-in) is a later ParallelStage composite (M6), not a
// graph runtime.
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// StageDeps are the shared dependencies the engine injects into every stage's
// Run and Gate: the LLM client, the tool registry, and the pipeline's shared
// blackboard. A stage typically uses these to drive an agent (see RunAgent) and
// to read/write inter-stage state.
type StageDeps struct {
	LLM      llm.LLM
	Registry *tool.Registry
	State    *State
	// History, when set by the engine, appends messages to the pipeline's durable
	// log (M5-003). RunAgent wires it into the agent's transcript sink
	// (WithTranscriptSink), so every message is recorded as produced — the
	// lossless full transcript, kept behind M4's lossy compaction projection.
	// nil ⇒ no durable log (e.g. a store-less pipeline).
	History func(msgs []llm.Message)
}

// Stage is one typed node in the pipeline. In is the stage's input (the previous
// stage's output, or the pipeline input for the entry stage); Out is what it
// produces. Run does the work; Verify is an optional programmatic check folded
// into the default gate; Policy is the stage's independent M4 context strategy;
// Gate (optional) decides pass/fail/await-human on the output.
//
// Tools is the stage's tool allowlist: the engine hands this stage's Run and
// Gate a StageDeps whose Registry is the shared registry filtered to exactly
// these names — so different stages/agents expose different tool subsets to the
// LLM (multi-agent division of labor: a coding stage gets file/shell, a review
// stage stays read-only). Empty ⇒ the full shared registry (backward compatible).
//
// Stage[In, Out] is the TYPED authoring surface. The engine drives the
// type-erased node it compiles to (see compile); In/Out are asserted once at the
// stage boundary, never threaded through the engine.
type Stage[In, Out any] struct {
	Name   string
	Run    func(ctx context.Context, in In, deps StageDeps) (Out, error)
	Verify func(ctx context.Context, out Out) error
	Policy agent.ContextPolicy
	Gate   Gate
	Tools  []string
}

// node is the type-erased form of a Stage that the FSM executes. The In/Out
// types live captured inside the run and gate closures.
type node struct {
	name   string
	run    func(ctx context.Context, in any, deps StageDeps) (any, error)
	gate   Gate
	policy agent.ContextPolicy
	tools  []string
}

// compile converts a typed Stage into the engine's type-erased node. The input
// is asserted to In at the boundary; a nil input (entry stage with no prior
// output) becomes the zero In. Verify, if set, is folded into a gate unless an
// explicit Gate is provided.
func (s Stage[In, Out]) compile() (node, error) {
	if s.Name == "" {
		return node{}, fmt.Errorf("pipeline: stage has empty name")
	}
	if s.Run == nil {
		return node{}, fmt.Errorf("pipeline: stage %q has nil Run", s.Name)
	}

	gate := s.Gate
	if gate == nil {
		verify := s.Verify
		gate = func(ctx context.Context, out any, _ StageDeps) (GateResult, error) {
			if verify == nil {
				return GateResult{Status: GatePass}, nil
			}
			typed, ok := out.(Out)
			if !ok {
				return GateResult{}, fmt.Errorf("pipeline: stage %q verify: output type %T is not %T", s.Name, out, *new(Out))
			}
			if err := verify(ctx, typed); err != nil {
				return GateResult{Status: GateFail, Reason: err.Error()}, nil
			}
			return GateResult{Status: GatePass}, nil
		}
	}

	run := func(ctx context.Context, in any, deps StageDeps) (any, error) {
		var typed In
		if in != nil {
			t, ok := in.(In)
			if !ok {
				return nil, fmt.Errorf("pipeline: stage %q: input type %T is not %T", s.Name, in, *new(In))
			}
			typed = t
		}
		return s.Run(ctx, typed, deps)
	}

	return node{name: s.Name, run: run, gate: gate, policy: s.Policy, tools: s.Tools}, nil
}

// filterRegistry returns a view of reg containing only the named tools. An empty
// name list (or nil reg) returns reg unchanged. A name absent from reg is an
// error — a stage must not silently run with fewer tools than it declared.
func filterRegistry(reg *tool.Registry, names []string) (*tool.Registry, error) {
	if reg == nil || len(names) == 0 {
		return reg, nil
	}
	sub := tool.NewRegistry()
	for _, n := range names {
		tl, ok := reg.Get(n)
		if !ok {
			return nil, fmt.Errorf("pipeline: stage tool %q not found in registry", n)
		}
		if err := sub.Register(tl); err != nil {
			return nil, err
		}
	}
	return sub, nil
}

// stageDeps returns deps with Registry narrowed to the stage's tool allowlist.
func (p *Pipeline) stageDeps(n node) (StageDeps, error) {
	deps := p.deps
	if len(n.tools) > 0 {
		sub, err := filterRegistry(p.deps.Registry, n.tools)
		if err != nil {
			return StageDeps{}, err
		}
		deps.Registry = sub
	}
	return deps, nil
}

// GateStatus is the outcome of a verification gate.
type GateStatus int

const (
	// GatePass lets the pipeline proceed to routing.
	GatePass GateStatus = iota
	// GateFail retries the stage (up to the pipeline's maxRetries).
	GateFail
	// GateAwaitHuman pauses the pipeline for human approval; the engine
	// persists state and ends the current Run until Resume is called.
	GateAwaitHuman
)

func (g GateStatus) String() string {
	switch g {
	case GatePass:
		return "pass"
	case GateFail:
		return "fail"
	case GateAwaitHuman:
		return "await_human"
	default:
		return "unknown"
	}
}

// GateResult is what a Gate returns. Reason is surfaced in events and audit.
type GateResult struct {
	Status GateStatus
	Reason string
}

// Gate evaluates a stage's (type-erased) output and decides whether the pipeline
// may proceed. It is a function type — a lightweight strategy, per the arch doc.
type Gate func(ctx context.Context, out any, deps StageDeps) (GateResult, error)

// AutoGate wraps a programmatic check (build / lint / test / etc.). A nil error
// passes; a non-nil error fails the gate (triggering a retry).
func AutoGate(check func(ctx context.Context, out any) error) Gate {
	return func(ctx context.Context, out any, _ StageDeps) (GateResult, error) {
		if err := check(ctx, out); err != nil {
			return GateResult{Status: GateFail, Reason: err.Error()}, nil
		}
		return GateResult{Status: GatePass}, nil
	}
}

// HumanGate always requests human approval: it returns GateAwaitHuman, pausing
// the pipeline (the engine persists state and waits for Resume).
func HumanGate() Gate {
	return func(context.Context, any, StageDeps) (GateResult, error) {
		return GateResult{Status: GateAwaitHuman, Reason: "awaiting human approval"}, nil
	}
}

// LLMReviewGate asks the LLM to judge the output against criteria. The model is
// instructed to answer "PASS" or "FAIL: <reason>"; anything not clearly PASS
// fails the gate.
func LLMReviewGate(client llm.LLM, criteria string) Gate {
	return func(ctx context.Context, out any, _ StageDeps) (GateResult, error) {
		prompt := []llm.Message{
			llm.SystemMessage("You are a strict reviewer. Judge whether the OUTPUT satisfies the CRITERIA. " +
				`Reply with exactly "PASS" on the first line if it does, or "FAIL" followed by a brief reason if it does not.`),
			llm.UserMessage(fmt.Sprintf("CRITERIA:\n%s\n\nOUTPUT:\n%v", criteria, out)),
		}
		resp, err := client.Chat(ctx, prompt)
		if err != nil {
			return GateResult{}, fmt.Errorf("llm review gate: %w", err)
		}
		text := strings.TrimSpace(resp.Message.Content)
		if strings.HasPrefix(strings.ToUpper(text), "PASS") {
			return GateResult{Status: GatePass, Reason: text}, nil
		}
		return GateResult{Status: GateFail, Reason: text}, nil
	}
}

// RunAgent is a convenience for stages that drive an agent: it builds a
// SimpleAgent over deps with the given system prompt and context policy, runs
// task to completion, and returns the final response text. Intermediate
// think/tool events are consumed internally; stages that need to surface them
// should construct and drive the agent directly.
func RunAgent(ctx context.Context, deps StageDeps, task, system string, policy agent.ContextPolicy) (string, error) {
	reg := deps.Registry
	if reg == nil {
		reg = tool.NewRegistry()
	}
	opts := []agent.Option{
		agent.WithSystemPrompt(system),
		agent.WithContextPolicy(policy),
	}
	// Capture the full conversation transcript to the pipeline's durable log as
	// it is produced (sent context may later be compacted; stored stays lossless).
	if deps.History != nil {
		sink := deps.History
		opts = append(opts, agent.WithTranscriptSink(func(m llm.Message) { sink([]llm.Message{m}) }))
	}
	a := agent.New(deps.LLM, reg, opts...)
	var final string
	for ev, err := range a.Run(ctx, task) {
		if err != nil {
			return "", err
		}
		if ev.Type == agent.EventResponse {
			final = ev.Content
		}
	}
	return final, nil
}
