package pipeline

import "github.com/Misakaworstlv999/goforge/pkg/agent"

// EventType discriminates the variants of a pipeline Event. Like Ring 3's
// agent.Event it is a tagged union, keeping the FSM's emissions flat to switch on.
type EventType int

const (
	// EventStageEnter: the engine began executing a stage.
	EventStageEnter EventType = iota
	// EventStageOutput: a stage produced output (before its gate runs).
	EventStageOutput
	// EventGatePass: a stage's gate passed; routing follows.
	EventGatePass
	// EventGateFail: a stage's gate failed; a retry or failure follows.
	EventGateFail
	// EventRetry: the engine is re-running a stage after a gate failure.
	EventRetry
	// EventPaused: a human-approval gate paused the pipeline; state is persisted.
	EventPaused
	// EventDone: the pipeline reached a terminal Done route.
	EventDone
	// EventFailed: a stage exhausted retries or hit an unrecoverable error.
	EventFailed
	// EventAgent: a forwarded inner agent.Event (optional progress detail).
	EventAgent
	// EventControl: a controller steered the run (steer/redirect) at a safe point.
	EventControl
	// EventCanceled: a controller canceled the run (terminal).
	EventCanceled
)

func (t EventType) String() string {
	switch t {
	case EventStageEnter:
		return "stage_enter"
	case EventStageOutput:
		return "stage_output"
	case EventGatePass:
		return "gate_pass"
	case EventGateFail:
		return "gate_fail"
	case EventRetry:
		return "retry"
	case EventPaused:
		return "paused"
	case EventDone:
		return "done"
	case EventFailed:
		return "failed"
	case EventAgent:
		return "agent"
	case EventControl:
		return "control"
	case EventCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// Event is a single observation emitted by Pipeline.Run / Resume. Stage names
// the relevant stage; Detail carries a human-readable description (e.g. a gate
// reason); Agent carries a forwarded inner agent event when Type == EventAgent.
type Event struct {
	Type   EventType
	Stage  string
	Detail string
	Agent  *agent.Event
}

func stageEnterEvent(stage string) Event {
	return Event{Type: EventStageEnter, Stage: stage}
}

func stageOutputEvent(stage, detail string) Event {
	return Event{Type: EventStageOutput, Stage: stage, Detail: detail}
}

func gatePassEvent(stage, reason string) Event {
	return Event{Type: EventGatePass, Stage: stage, Detail: reason}
}

func gateFailEvent(stage, reason string) Event {
	return Event{Type: EventGateFail, Stage: stage, Detail: reason}
}

func retryEvent(stage, detail string) Event {
	return Event{Type: EventRetry, Stage: stage, Detail: detail}
}

func pausedEvent(stage, reason string) Event {
	return Event{Type: EventPaused, Stage: stage, Detail: reason}
}

func doneEvent(stage string) Event {
	return Event{Type: EventDone, Stage: stage}
}

func failedEvent(stage, detail string) Event {
	return Event{Type: EventFailed, Stage: stage, Detail: detail}
}

func controlEvent(stage, detail string) Event {
	return Event{Type: EventControl, Stage: stage, Detail: detail}
}

func canceledEvent(stage, reason string) Event {
	return Event{Type: EventCanceled, Stage: stage, Detail: reason}
}
