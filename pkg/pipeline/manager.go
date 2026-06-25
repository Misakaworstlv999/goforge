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

// Events returns a one-shot snapshot of the run's events so far (replay only).
func (m *Manager) Events(runID string) ([]Event, error) {
	replay, _, cancel, err := m.Subscribe(runID)
	if err != nil {
		return nil, err
	}
	cancel()
	return replay, nil
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

// Checkpoints returns the run's checkpoint lineage (seq-ordered), the addressable
// points rewind/fork can target. Requires a store with lineage.
func (m *Manager) Checkpoints(ctx context.Context, runID string) ([]CheckpointInfo, error) {
	h, err := m.handle(runID)
	if err != nil {
		return nil, err
	}
	if h.pipeline.store == nil {
		return nil, fmt.Errorf("pipeline: run %q has no store (lineage unavailable)", runID)
	}
	return h.pipeline.store.ListCheckpoints(ctx, runID)
}

// Rewind re-runs runID from an earlier checkpoint (time-travel): same id, a fresh
// run driven from the state at seq, with optional guidance injected. Requires a
// store with lineage (SaveStep).
func (m *Manager) Rewind(runID string, seq int, note string) error {
	_, err := m.replay(runID, runID, seq, note)
	return err
}

// Fork starts a NEW run continuing from an earlier checkpoint of srcID (explore
// an alternate path without disturbing the original). An empty newID is generated.
func (m *Manager) Fork(srcID, newID string, seq int) (string, error) {
	return m.replay(srcID, newID, seq, "")
}

func (m *Manager) replay(srcID, dstID string, seq int, note string) (string, error) {
	src, err := m.handle(srcID)
	if err != nil {
		return "", err
	}
	if src.pipeline.store == nil {
		return "", fmt.Errorf("pipeline: run %q has no store (lineage unavailable)", srcID)
	}
	st, err := src.pipeline.store.LoadAt(context.Background(), srcID, seq)
	if err != nil {
		return "", fmt.Errorf("pipeline: rewind %q@%d: %w", srcID, seq, err)
	}

	m.mu.Lock()
	if dstID == "" {
		m.seq++
		dstID = fmt.Sprintf("%s-fork-%d", srcID, m.seq)
	}
	st.PipelineID = dstID
	if note != "" { // inject guidance the re-run's stages can read via SteerKey
		if st.Blackboard == nil {
			st.Blackboard = map[string]any{}
		}
		prev, _ := st.Blackboard[SteerKey].([]any)
		st.Blackboard[SteerKey] = append(prev, note)
	}
	p := m.factory(dstID)
	runCtx, cancel := context.WithCancel(context.Background())
	h := &runHandle{
		id:       dstID,
		control:  make(chan Control, 16),
		hub:      newEventHub(),
		cancel:   cancel,
		pipeline: p,
		done:     make(chan struct{}),
	}
	m.runs[dstID] = h // a rewind replaces the prior handle for the same id
	m.mu.Unlock()

	go func() {
		defer close(h.done)
		defer h.hub.close()
		for ev := range p.runControlledFrom(runCtx, st, h.control) {
			h.hub.publish(ev)
		}
	}()
	return dstID, nil
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
