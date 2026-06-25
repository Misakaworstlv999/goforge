package pipeline

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// Engine errors surfaced as the terminal error of Run / Resume.
var (
	ErrNoStages             = errors.New("pipeline: no stages registered")
	ErrUnknownStage         = errors.New("pipeline: unknown stage")
	ErrStageRetriesExceeded = errors.New("pipeline: stage retries exceeded")
	ErrNotPaused            = errors.New("pipeline: not paused")
	ErrNoStore              = errors.New("pipeline: operation requires a checkpoint store")
	ErrMaxStepsExceeded     = errors.New("pipeline: max steps exceeded")
)

const (
	defaultMaxRetries = 2
	defaultMaxSteps   = 100 // runaway guard for cyclic routing (cf. Eino MaxRunSteps)
)

// Route is the result of a RouteFunc: the next stage to run, or Done to finish.
type Route struct {
	Next string
	Done bool
}

// RouteFunc decides where to go after a stage's gate passes. out is the stage's
// output; st is the shared blackboard. A RouteFunc may return any registered
// stage name — including an earlier one — which is what makes the FSM a cyclic
// graph (retry-from-scratch, review-bounce-to-coding). Done (or an empty Next)
// ends the pipeline.
type RouteFunc func(ctx context.Context, out any, st *State) (Route, error)

// Decision settles a paused human-approval gate when passed to Resume.
type Decision struct {
	Approved bool
	Reason   string
}

// Pipeline is the FSM engine. Construct with New, register stages with AddStage,
// optionally override routing with Route, then Run / Resume. It is deliberately
// sequential: one stage is active at a time, which keeps checkpoint/resume/audit
// linear and deterministic.
type Pipeline struct {
	deps       StageDeps
	nodes      map[string]node
	order      []string
	routes     map[string]RouteFunc
	store      CheckpointStore
	maxRetries int
	maxSteps   int
}

// Option configures a Pipeline.
type Option func(*Pipeline)

// WithStore sets the checkpoint store (state persistence, durable log, audit).
// Without a store the pipeline still runs and emits events, but cannot Resume.
func WithStore(s CheckpointStore) Option { return func(p *Pipeline) { p.store = s } }

// WithMaxRetries caps per-stage gate-fail retries (default 2). 0 ⇒ no retries.
func WithMaxRetries(n int) Option {
	return func(p *Pipeline) {
		if n >= 0 {
			p.maxRetries = n
		}
	}
}

// WithMaxSteps bounds the total number of stage executions in one Run/Resume,
// guarding against a buggy RouteFunc that cycles forever (default 100). Non-
// positive values are ignored.
func WithMaxSteps(n int) Option {
	return func(p *Pipeline) {
		if n > 0 {
			p.maxSteps = n
		}
	}
}

// New constructs a Pipeline over the given shared dependencies. If deps.State is
// nil a fresh blackboard is created.
func New(deps StageDeps, opts ...Option) *Pipeline {
	p := &Pipeline{
		deps:       deps,
		nodes:      make(map[string]node),
		routes:     make(map[string]RouteFunc),
		maxRetries: defaultMaxRetries,
		maxSteps:   defaultMaxSteps,
	}
	for _, o := range opts {
		o(p)
	}
	if p.deps.State == nil {
		p.deps.State = NewState()
	}
	return p
}

// AddStage registers a typed stage. Stages run in registration order by default;
// override transitions with Route. The first registered stage is the entry
// point. It is a free function (not a method) because Go methods cannot carry
// their own type parameters.
func AddStage[In, Out any](p *Pipeline, s Stage[In, Out]) error {
	n, err := s.compile()
	if err != nil {
		return err
	}
	if _, exists := p.nodes[n.name]; exists {
		return fmt.Errorf("pipeline: duplicate stage %q", n.name)
	}
	p.nodes[n.name] = n
	p.order = append(p.order, n.name)
	return nil
}

// Route overrides the routing applied after stage's gate passes.
func (p *Pipeline) Route(stage string, fn RouteFunc) {
	p.routes[stage] = fn
}

// State returns the engine's shared blackboard.
func (p *Pipeline) State() *State { return p.deps.State }

// Run executes the pipeline from its entry stage with the given input, emitting
// events as a pull-based iterator. It terminates with a Done event (success), a
// Paused event (awaiting human approval), or a Failed event paired with an error.
func (p *Pipeline) Run(ctx context.Context, pipelineID string, input any) iter.Seq2[Event, error] {
	return p.runControlled(ctx, pipelineID, input, nil)
}

// runControlledFrom drives an EXISTING (loaded) state rather than a fresh one —
// the basis for rewind/fork (time-travel). It restores the blackboard, marks the
// run running, and re-executes from st.CurrentStage.
func (p *Pipeline) runControlledFrom(ctx context.Context, st *PipelineState, control <-chan Control) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		p.bindHistory(ctx, st.PipelineID)
		p.deps.State.Load(st.Blackboard)
		st.Status = StatusRunning
		if st.RetryCount == nil {
			st.RetryCount = map[string]int{}
		}
		p.drive(ctx, st, control, yield)
	}
}

// runControlled is Run with an optional control channel attached (the Manager
// uses this for steerable runs). A nil control channel reproduces Run exactly.
func (p *Pipeline) runControlled(ctx context.Context, pipelineID string, input any, control <-chan Control) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if len(p.order) == 0 {
			yield(failedEvent("", ErrNoStages.Error()), ErrNoStages)
			return
		}
		p.bindHistory(ctx, pipelineID)
		st := &PipelineState{
			PipelineID:   pipelineID,
			CurrentStage: p.order[0],
			Status:       StatusRunning,
			RetryCount:   map[string]int{},
			StageInput:   input,
		}
		p.drive(ctx, st, control, yield)
	}
}

// Resume continues a paused pipeline. With an approving Decision the paused
// stage's gate is treated as passed and routing proceeds; with a rejecting
// Decision the stage is retried. Requires a store.
func (p *Pipeline) Resume(ctx context.Context, pipelineID string, decision Decision) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if p.store == nil {
			yield(failedEvent("", ErrNoStore.Error()), ErrNoStore)
			return
		}
		st, err := p.store.Load(ctx, pipelineID)
		if err != nil {
			yield(failedEvent("", err.Error()), err)
			return
		}
		if st.Status != StatusPaused {
			err := fmt.Errorf("%w: status %s", ErrNotPaused, st.Status)
			yield(failedEvent(st.CurrentStage, "pipeline not paused"), err)
			return
		}

		p.deps.State.Load(st.Blackboard)
		p.bindHistory(ctx, pipelineID)
		if st.RetryCount == nil {
			st.RetryCount = map[string]int{}
		}
		st.Status = StatusRunning
		p.audit(ctx, st, ActionResume, decision.Reason)

		if decision.Approved {
			done, rerr := p.advance(ctx, st, st.StageOutput, yield)
			if rerr != nil || done {
				return
			}
		} else if !p.bumpRetry(ctx, st, "human rejected", yield) {
			return
		}
		p.drive(ctx, st, nil, yield)
	}
}

// drive runs the main FSM loop starting by executing st.CurrentStage with
// st.StageInput. It returns when the pipeline completes, fails, pauses, or the
// consumer stops iterating.
func (p *Pipeline) drive(ctx context.Context, st *PipelineState, control <-chan Control, yield func(Event, error) bool) {
	for steps := 0; ; steps++ {
		// Stage safe point: apply any pending control (pause/resume/steer/
		// redirect/cancel). No-op when control is nil ⇒ unchanged behavior.
		if p.applyControl(ctx, st, control, yield) == ctlStop {
			return
		}
		if steps >= p.maxSteps {
			p.fail(ctx, st, "max steps exceeded", fmt.Errorf("stage %q: %w", st.CurrentStage, ErrMaxStepsExceeded), yield)
			return
		}
		n, ok := p.nodes[st.CurrentStage]
		if !ok {
			p.fail(ctx, st, "unknown stage", fmt.Errorf("%w: %q", ErrUnknownStage, st.CurrentStage), yield)
			return
		}

		deps, err := p.stageDeps(n)
		if err != nil {
			p.fail(ctx, st, err.Error(), fmt.Errorf("stage %q: %w", st.CurrentStage, err), yield)
			return
		}
		// Give agent-driven stages a step-granular control seam over the same
		// channel (drive is blocked here while the stage runs, so single reader).
		deps.Interrupt = p.agentInterrupt(control)

		p.audit(ctx, st, ActionEnter, "")
		if !yield(stageEnterEvent(st.CurrentStage), nil) {
			return
		}

		out, err := n.run(ctx, st.StageInput, deps)
		if err != nil {
			var ab *controlAbort
			if errors.As(err, &ab) {
				if ab.op == OpCancel {
					p.doCancel(ctx, st, ab.note, yield)
					return
				}
				// OpRedirect: route elsewhere and re-loop (no stage failure).
				if ab.stage != "" {
					st.CurrentStage = ab.stage
					st.StageInput = st.StageOutput
				}
				p.audit(ctx, st, ActionRedirect, ab.stage)
				if !yield(controlEvent(st.CurrentStage, "redirect (mid-agent): "+ab.stage+" "+ab.note), nil) {
					return
				}
				continue
			}
			p.fail(ctx, st, err.Error(), fmt.Errorf("stage %q: %w", st.CurrentStage, err), yield)
			return
		}
		st.StageOutput = out
		if !yield(stageOutputEvent(st.CurrentStage, describe(out)), nil) {
			return
		}

		res, err := n.gate(ctx, out, deps)
		if err != nil {
			p.fail(ctx, st, err.Error(), fmt.Errorf("stage %q gate: %w", st.CurrentStage, err), yield)
			return
		}

		switch res.Status {
		case GatePass:
			p.audit(ctx, st, ActionGatePass, res.Reason)
			if !yield(gatePassEvent(st.CurrentStage, res.Reason), nil) {
				return
			}
			done, rerr := p.advance(ctx, st, out, yield)
			if rerr != nil || done {
				return
			}
		case GateFail:
			p.audit(ctx, st, ActionGateFail, res.Reason)
			if !yield(gateFailEvent(st.CurrentStage, res.Reason), nil) {
				return
			}
			if !p.bumpRetry(ctx, st, res.Reason, yield) {
				return
			}
		case GateAwaitHuman:
			st.Status = StatusPaused
			_ = p.save(ctx, st)
			p.audit(ctx, st, ActionInterrupt, res.Reason)
			yield(pausedEvent(st.CurrentStage, res.Reason), nil)
			return
		}
	}
}

// advance routes after a passing gate. It returns done=true when the pipeline
// completes, or err!=nil when routing fails (both already emitted + persisted).
// Otherwise it repositions st at the next stage (horizontal hand-off: the next
// stage's input is this stage's output) and returns done=false.
func (p *Pipeline) advance(ctx context.Context, st *PipelineState, out any, yield func(Event, error) bool) (done bool, err error) {
	r, rerr := p.routeAfter(ctx, st.CurrentStage, out)
	if rerr != nil {
		p.fail(ctx, st, rerr.Error(), fmt.Errorf("routing after %q: %w", st.CurrentStage, rerr), yield)
		return false, rerr
	}
	if r.Done || r.Next == "" {
		st.Status = StatusCompleted
		_ = p.save(ctx, st)
		p.audit(ctx, st, ActionComplete, "")
		yield(doneEvent(st.CurrentStage), nil)
		return true, nil
	}
	st.CurrentStage = r.Next
	st.StageInput = out
	st.Status = StatusRunning
	_ = p.save(ctx, st)
	return false, nil
}

// bumpRetry increments the retry counter for the current stage and either emits
// a Retry event (returning true, keep looping) or fails the pipeline once the
// limit is exceeded (returning false).
func (p *Pipeline) bumpRetry(ctx context.Context, st *PipelineState, reason string, yield func(Event, error) bool) bool {
	st.RetryCount[st.CurrentStage]++
	if st.RetryCount[st.CurrentStage] > p.maxRetries {
		p.fail(ctx, st, "max retries exceeded", fmt.Errorf("stage %q: %w", st.CurrentStage, ErrStageRetriesExceeded), yield)
		return false
	}
	_ = p.save(ctx, st)
	detail := fmt.Sprintf("%s (attempt %d)", reason, st.RetryCount[st.CurrentStage])
	p.audit(ctx, st, ActionRetry, detail)
	return yield(retryEvent(st.CurrentStage, detail), nil)
}

// fail marks the pipeline failed, persists, audits, and emits the terminal event.
func (p *Pipeline) fail(ctx context.Context, st *PipelineState, detail string, err error, yield func(Event, error) bool) {
	st.Status = StatusFailed
	_ = p.save(ctx, st)
	p.audit(ctx, st, ActionFail, detail)
	yield(failedEvent(st.CurrentStage, detail), err)
}

func (p *Pipeline) routeAfter(ctx context.Context, stage string, out any) (Route, error) {
	if fn, ok := p.routes[stage]; ok {
		return fn(ctx, out, p.deps.State)
	}
	return p.defaultNext(stage), nil
}

// defaultNext routes to the next stage in registration order, or Done after the
// last stage.
func (p *Pipeline) defaultNext(stage string) Route {
	for i, name := range p.order {
		if name == stage && i+1 < len(p.order) {
			return Route{Next: p.order[i+1]}
		}
	}
	return Route{Done: true}
}

// save snapshots the blackboard into st and persists it (no-op without a store).
func (p *Pipeline) save(ctx context.Context, st *PipelineState) error {
	if p.store == nil {
		return nil
	}
	st.UpdatedAt = time.Now()
	st.Blackboard = p.deps.State.Snapshot()
	st.Seq++ // monotonic per transition → checkpoint lineage
	if err := p.store.Save(ctx, st); err != nil {
		return err
	}
	// Append to the lineage (best-effort: a lineage write failure must not abort
	// the run; the latest snapshot in Save is the correctness-critical one).
	_ = p.store.SaveStep(ctx, st)
	return nil
}

// audit appends an audit entry (best-effort; no-op without a store).
func (p *Pipeline) audit(ctx context.Context, st *PipelineState, action, detail string) {
	if p.store == nil {
		return
	}
	_ = p.store.Audit(ctx, AuditEntry{
		Timestamp:  time.Now(),
		PipelineID: st.PipelineID,
		Stage:      st.CurrentStage,
		Action:     action,
		Detail:     detail,
	})
}

// bindHistory wires the durable-log sink for this run into deps.History, so
// agents built via RunAgent record their full transcript to the store under
// this pipeline ID as messages are produced.
func (p *Pipeline) bindHistory(ctx context.Context, pipelineID string) {
	if p.store == nil {
		return
	}
	p.deps.History = func(msgs []llm.Message) {
		_ = p.store.AppendHistory(ctx, pipelineID, msgs)
	}
}

// describe renders a stage output for event detail, truncated for readability.
func describe(v any) string {
	s := fmt.Sprintf("%v", v)
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
