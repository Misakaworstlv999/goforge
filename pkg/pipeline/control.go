package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// controlAbort is the error an in-agent interrupt returns to abort a stage's
// agent for a control op that the agent itself can't apply (cancel ⇒ end the
// run; redirect ⇒ route elsewhere). drive recognizes it via errors.As and
// finalizes accordingly instead of treating it as a stage failure.
type controlAbort struct {
	op    ControlOp
	stage string
	note  string
}

func (e *controlAbort) Error() string {
	return fmt.Sprintf("pipeline: control abort op=%d stage=%q", e.op, e.stage)
}

// agentInterrupt adapts the run's control channel into an agent.Interrupt so
// control applies at each ReAct STEP boundary (fine-grained), not just between
// stages. Pause blocks; steer injects a message (and persists to the blackboard);
// cancel/redirect return a *controlAbort for drive to finalize. nil control ⇒
// nil interrupt (agent runs uninstrumented). Safe: drive is blocked inside the
// stage while the agent runs, so this is the channel's only reader meanwhile.
func (p *Pipeline) agentInterrupt(control <-chan Control) agent.Interrupt {
	if control == nil {
		return nil
	}
	return func(ctx context.Context, _ int) ([]llm.Message, error) {
		var inject []llm.Message
		for {
			select {
			case <-ctx.Done():
				return nil, &controlAbort{op: OpCancel, note: ctx.Err().Error()}
			case c := <-control:
				switch c.Op {
				case OpCancel:
					return nil, &controlAbort{op: OpCancel, note: c.Note}
				case OpRedirect:
					return nil, &controlAbort{op: OpRedirect, stage: c.Stage, note: c.Note}
				case OpSteer:
					p.deps.State.SetReducer(SteerKey, AppendReducer)
					p.deps.State.Set(SteerKey, c.Note)
					inject = append(inject, llm.UserMessage("[steer] "+c.Note))
				case OpPause:
					if ab := p.agentWaitResume(ctx, control); ab != nil {
						return nil, ab
					}
				}
			default:
				return inject, nil
			}
		}
	}
}

// agentWaitResume blocks a paused agent until OpResume (returns nil), OpCancel/
// OpRedirect (returns *controlAbort), or ctx cancellation.
func (p *Pipeline) agentWaitResume(ctx context.Context, control <-chan Control) *controlAbort {
	for {
		select {
		case <-ctx.Done():
			return &controlAbort{op: OpCancel, note: ctx.Err().Error()}
		case c := <-control:
			switch c.Op {
			case OpResume:
				return nil
			case OpCancel:
				return &controlAbort{op: OpCancel, note: c.Note}
			case OpRedirect:
				return &controlAbort{op: OpRedirect, stage: c.Stage, note: c.Note}
			case OpSteer:
				p.deps.State.SetReducer(SteerKey, AppendReducer)
				p.deps.State.Set(SteerKey, c.Note)
			}
		}
	}
}

// SteerKey is the shared-blackboard key under which OpSteer notes accumulate
// (with AppendReducer). A stage that wants to honor live guidance reads it via a
// ContextSource bound to this key — see SteerSource.
const SteerKey = "steer"

// SteerSource renders the accumulated steering guidance (SteerKey notes, from
// OpSteer and from rewind/fork's injected note) as a read-only context message,
// so a stage's agent actually HONORS operator guidance. Without a source like
// this, steer/rewind notes sit unread on the blackboard and never reach the LLM.
// Bind it into a RunAgent-based stage's ContextPolicy. Returns nil (injects
// nothing) when there is no guidance.
func SteerSource(st *State) agent.ContextSource {
	return func(context.Context, string) ([]llm.Message, error) {
		v, ok := st.Get(SteerKey)
		if !ok {
			return nil, nil
		}
		notes := toStringSlice(v)
		if len(notes) == 0 {
			return nil, nil
		}
		var b strings.Builder
		b.WriteString("Operator guidance for this run (honor it):\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		return []llm.Message{llm.SystemMessage(b.String())}, nil
	}
}

// toStringSlice normalizes a SteerKey value (accumulated as []any by AppendReducer,
// or a bare string / []string) into a string slice.
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, fmt.Sprintf("%v", e))
		}
		return out
	case string:
		return []string{t}
	case nil:
		return nil
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}

// ControlOp is a steering operation applied to a running pipeline at a stage
// safe point (between stages), preserving the one-stage-at-a-time determinism.
type ControlOp int

const (
	// OpPause halts the run at the next safe point until OpResume or OpCancel.
	OpPause ControlOp = iota
	// OpResume continues a paused run.
	OpResume
	// OpCancel terminates the run (terminal StatusCanceled).
	OpCancel
	// OpRedirect routes execution to Control.Stage at the next safe point
	// (exploits the cyclic FSM; e.g. send review back to coding).
	OpRedirect
	// OpSteer injects Control.Note into the blackboard (SteerKey) for stages to read.
	OpSteer
)

// Control is one steering command delivered to a run's control channel.
type Control struct {
	Op    ControlOp
	Stage string // OpRedirect: target stage name
	Note  string // OpSteer: guidance text; OpRedirect/OpCancel: reason
}

// ctlAction is the internal result of applying pending control at a safe point.
type ctlAction int

const (
	ctlContinue ctlAction = iota // proceed with the loop
	ctlStop                      // terminate drive (canceled, or consumer broke)
)

// applyControl drains pending control commands at a stage safe point. It is a
// no-op when control is nil (zero value ⇒ today's behavior). Non-pausing
// commands are applied and the loop continues; OpPause blocks (via waitResume)
// until OpResume/OpCancel. Returns ctlStop if the run was canceled or the
// consumer stopped iterating.
func (p *Pipeline) applyControl(ctx context.Context, st *PipelineState, control <-chan Control, yield func(Event, error) bool) ctlAction {
	if control == nil {
		return ctlContinue
	}
	for {
		select {
		case c, ok := <-control:
			if !ok {
				return ctlContinue // controller went away; run autonomously
			}
			if act, handled := p.applyOne(ctx, st, c, control, yield); handled {
				if act == ctlStop {
					return ctlStop
				}
			}
		default:
			return ctlContinue // nothing pending
		}
	}
}

// applyOne applies a single control command. handled is false for no-ops (e.g.
// OpResume while not paused) so the caller keeps draining.
func (p *Pipeline) applyOne(ctx context.Context, st *PipelineState, c Control, control <-chan Control, yield func(Event, error) bool) (act ctlAction, handled bool) {
	switch c.Op {
	case OpCancel:
		return p.doCancel(ctx, st, c.Note, yield), true
	case OpRedirect:
		if c.Stage != "" {
			st.CurrentStage = c.Stage
			st.StageInput = st.StageOutput
		}
		p.audit(ctx, st, ActionRedirect, c.Stage)
		if !yield(controlEvent(st.CurrentStage, "redirect: "+c.Stage+" "+c.Note), nil) {
			return ctlStop, true
		}
		return ctlContinue, true
	case OpSteer:
		p.deps.State.SetReducer(SteerKey, AppendReducer)
		p.deps.State.Set(SteerKey, c.Note)
		p.audit(ctx, st, ActionSteer, c.Note)
		if !yield(controlEvent(st.CurrentStage, "steer: "+c.Note), nil) {
			return ctlStop, true
		}
		return ctlContinue, true
	case OpPause:
		st.Status = StatusPaused
		_ = p.save(ctx, st)
		p.audit(ctx, st, ActionInterrupt, "paused by controller")
		if !yield(pausedEvent(st.CurrentStage, "paused by controller"), nil) {
			return ctlStop, true
		}
		act := p.waitResume(ctx, st, control, yield)
		if act == ctlContinue {
			st.Status = StatusRunning
		}
		return act, true
	default: // OpResume with no pause in effect
		return ctlContinue, false
	}
}

// waitResume blocks a paused run until OpResume (continue) or OpCancel (stop).
// OpSteer is still honored while paused; other ops are ignored. A closed channel
// resumes (controller gone).
func (p *Pipeline) waitResume(ctx context.Context, st *PipelineState, control <-chan Control, yield func(Event, error) bool) ctlAction {
	for {
		select {
		case <-ctx.Done():
			return p.doCancel(ctx, st, ctx.Err().Error(), yield)
		case c, ok := <-control:
			if !ok {
				return ctlContinue
			}
			switch c.Op {
			case OpResume:
				return ctlContinue
			case OpCancel:
				return p.doCancel(ctx, st, c.Note, yield)
			case OpSteer:
				p.deps.State.SetReducer(SteerKey, AppendReducer)
				p.deps.State.Set(SteerKey, c.Note)
				p.audit(ctx, st, ActionSteer, c.Note)
				if !yield(controlEvent(st.CurrentStage, "steer: "+c.Note), nil) {
					return ctlStop
				}
			}
		}
	}
}

// doCancel marks the run canceled, persists, audits, and emits the terminal event.
func (p *Pipeline) doCancel(ctx context.Context, st *PipelineState, reason string, yield func(Event, error) bool) ctlAction {
	st.Status = StatusCanceled
	_ = p.save(ctx, st)
	p.audit(ctx, st, ActionCancel, reason)
	yield(canceledEvent(st.CurrentStage, reason), nil)
	return ctlStop
}
