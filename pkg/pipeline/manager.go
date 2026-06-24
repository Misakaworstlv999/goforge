package pipeline

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Manager makes pipeline runs first-class, addressable, observable, and
// steerable resources. Each Trigger starts a run in the background (its own
// goroutine, control channel, and event hub), addressable by runID; observers
// Subscribe to its events and controllers Pause/Resume/Steer/Redirect/Cancel it.
// Humans (CLI/HTTP) and agents (the control tool set) drive it identically.
//
// It takes a factory that builds a FRESH Pipeline per run, so concurrent runs
// get isolated blackboards/state (e.g. func(id) *Pipeline { return
// workflow.BuildDevWorkflow(freshDeps, cfg) }).
type Manager struct {
	factory func(runID string) *Pipeline
	mu      sync.Mutex
	runs    map[string]*runHandle
	seq     int
}

type runHandle struct {
	id       string
	control  chan Control
	hub      *eventHub
	cancel   context.CancelFunc
	pipeline *Pipeline
	done     chan struct{}
}

// NewManager builds a Manager over a per-run Pipeline factory.
func NewManager(factory func(runID string) *Pipeline) *Manager {
	return &Manager{factory: factory, runs: make(map[string]*runHandle)}
}

// Trigger starts a new run with the given input and returns its id. An empty
// runID is auto-generated; a duplicate id is rejected. The run executes in the
// background (its own context derived from Background, so it outlives the
// caller's request) and persists via the pipeline's store as it goes.
func (m *Manager) Trigger(runID string, input any) (string, error) {
	m.mu.Lock()
	if runID == "" {
		m.seq++
		runID = fmt.Sprintf("run-%d", m.seq)
	}
	if _, exists := m.runs[runID]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("pipeline: run %q already exists", runID)
	}
	p := m.factory(runID)
	runCtx, cancel := context.WithCancel(context.Background())
	h := &runHandle{
		id:       runID,
		control:  make(chan Control, 16),
		hub:      newEventHub(),
		cancel:   cancel,
		pipeline: p,
		done:     make(chan struct{}),
	}
	m.runs[runID] = h
	m.mu.Unlock()

	go func() {
		defer close(h.done)
		defer h.hub.close()
		for ev := range p.runControlled(runCtx, runID, input, h.control) {
			h.hub.publish(ev)
		}
	}()
	return runID, nil
}

// Subscribe returns the run's events so far (replay) plus a live channel and a
// cancel func. Used by HTTP SSE and the control tools.
func (m *Manager) Subscribe(runID string) (replay []Event, live <-chan Event, cancel func(), err error) {
	h, err := m.handle(runID)
	if err != nil {
		return nil, nil, nil, err
	}
	replay, live, cancel = h.hub.subscribe()
	return replay, live, cancel, nil
}

// State returns the run's persisted FSM snapshot (requires the pipeline to have
// a checkpoint store).
func (m *Manager) State(ctx context.Context, runID string) (*PipelineState, error) {
	h, err := m.handle(runID)
	if err != nil {
		return nil, err
	}
	if h.pipeline.store == nil {
		return nil, fmt.Errorf("pipeline: run %q has no store", runID)
	}
	return h.pipeline.store.Load(ctx, runID)
}

// List returns the ids of all known runs (sorted).
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.runs))
	for id := range m.runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Wait blocks until the run finishes (test/CLI helper).
func (m *Manager) Wait(runID string) {
	m.mu.Lock()
	h := m.runs[runID]
	m.mu.Unlock()
	if h != nil {
		<-h.done
	}
}

// Control ops — delivered to the run's control channel, applied at its next
// stage safe point.
func (m *Manager) Pause(runID string) error  { return m.signal(runID, Control{Op: OpPause}) }
func (m *Manager) Resume(runID string) error { return m.signal(runID, Control{Op: OpResume}) }
func (m *Manager) Steer(runID, note string) error {
	return m.signal(runID, Control{Op: OpSteer, Note: note})
}
func (m *Manager) Redirect(runID, stage, note string) error {
	return m.signal(runID, Control{Op: OpRedirect, Stage: stage, Note: note})
}
func (m *Manager) Cancel(runID, reason string) error {
	return m.signal(runID, Control{Op: OpCancel, Note: reason})
}

// Close cancels all running runs' contexts (manager shutdown backstop).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.runs {
		h.cancel()
	}
}

func (m *Manager) handle(runID string) (*runHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.runs[runID]
	if !ok {
		return nil, fmt.Errorf("pipeline: no run %q", runID)
	}
	return h, nil
}

// signal delivers a control command, failing if the run has already finished.
func (m *Manager) signal(runID string, c Control) error {
	h, err := m.handle(runID)
	if err != nil {
		return err
	}
	select {
	case <-h.done:
		return fmt.Errorf("pipeline: run %q already finished", runID)
	case h.control <- c:
		return nil
	}
}
