package pipeline

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// TempPrefix marks blackboard keys as invocation-scoped: they are visible during
// a run but excluded from checkpoint snapshots, so they never persist across
// restarts (the ADK `temp:` convention).
const TempPrefix = "temp:"

// Reducer merges a new write into the existing value for a key. The default
// (no reducer) is replace. Reducers let repeated or accumulated writes combine
// instead of clobbering (cf. tRPC-Agent-Go per-field StateReducer) — useful for
// fan-in/accumulation patterns.
type Reducer func(old, new any) any

// AppendReducer accumulates successive writes into a []any slice.
func AppendReducer(old, new any) any {
	switch v := old.(type) {
	case nil:
		return []any{new}
	case []any:
		return append(v, new)
	default:
		return []any{old, new}
	}
}

// State is the pipeline's shared blackboard: a concurrency-safe, namespaced
// key→value region that stages read (via StateSource, the M4 ContextSource seam)
// and write (via stage output). This is the inter-agent memory-SHARING channel,
// distinct from the horizontal Stage[In,Out] hand-off.
type State struct {
	mu       sync.RWMutex
	data     map[string]any
	reducers map[string]Reducer
}

// NewState returns an empty blackboard.
func NewState() *State {
	return &State{data: make(map[string]any), reducers: make(map[string]Reducer)}
}

// Set writes a value, applying the key's reducer if one is registered.
func (s *State) Set(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.reducers[key]; ok {
		s.data[key] = r(s.data[key], val)
		return
	}
	s.data[key] = val
}

// Get returns the value for key and whether it is present.
func (s *State) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

// SetReducer registers a merge strategy for a key. Subsequent Set calls on that
// key combine the old and new values through r instead of replacing.
func (s *State) SetReducer(key string, r Reducer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reducers[key] = r
}

// Snapshot returns a shallow copy of the persistable blackboard, excluding
// temp:-prefixed (invocation-scoped) keys.
func (s *State) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.data))
	for k, v := range s.data {
		if strings.HasPrefix(k, TempPrefix) {
			continue
		}
		out[k] = v
	}
	return out
}

// Load merges a persisted snapshot back into the blackboard (used on resume).
func (s *State) Load(data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.data, data)
}

// StateSource adapts blackboard keys into an agent ContextSource, letting a
// stage's agent read shared state through the M4-005 seam without changing the
// Agent interface. With no keys, all non-temp keys are rendered (unordered);
// with explicit keys, only those present are rendered in the given order.
// Returns nil when there is nothing to inject.
func StateSource(st *State, keys ...string) agent.ContextSource {
	return func(context.Context, string) ([]llm.Message, error) {
		var b strings.Builder
		if len(keys) == 0 {
			for k, v := range st.Snapshot() {
				fmt.Fprintf(&b, "- %s: %v\n", k, v)
			}
		} else {
			for _, k := range keys {
				if v, ok := st.Get(k); ok {
					fmt.Fprintf(&b, "- %s: %v\n", k, v)
				}
			}
		}
		if b.Len() == 0 {
			return nil, nil
		}
		return []llm.Message{llm.SystemMessage("Shared pipeline context:\n" + b.String())}, nil
	}
}

// HistorySource returns an agent ContextSource that injects the pipeline's
// persisted transcript as prior context — the EXPLICIT opt-in for cross-stage
// continuity. Unlike ADK/tRPC, which auto-share one session across all agents,
// a GoForge stage sees upstream conversation only if it adds this source to its
// ContextPolicy (honoring stage-aware context loading: pull deliberately, don't
// passively accumulate). The transcript is RENDERED into one read-only context
// message (not injected as raw role-tagged messages), so a prior agent's
// tool_call/result pairing can never corrupt this agent's context. limit ≤ 0
// injects the whole transcript; otherwise the most recent `limit` messages. A
// read error aborts the run (strict, like other sources).
func HistorySource(store CheckpointStore, pipelineID string, limit int) agent.ContextSource {
	return func(ctx context.Context, _ string) ([]llm.Message, error) {
		msgs, err := store.History(ctx, pipelineID)
		if err != nil {
			return nil, fmt.Errorf("loading pipeline history for %q: %w", pipelineID, err)
		}
		if len(msgs) == 0 {
			return nil, nil
		}
		if limit > 0 && len(msgs) > limit {
			msgs = msgs[len(msgs)-limit:]
		}
		var b strings.Builder
		for _, m := range msgs {
			if m.Content == "" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
		if b.Len() == 0 {
			return nil, nil
		}
		return []llm.Message{llm.SystemMessage("Prior pipeline conversation (read-only context):\n" + b.String())}, nil
	}
}

// Status is the lifecycle state of a pipeline run.
type Status int

const (
	// StatusRunning: the pipeline is executing or ready to execute.
	StatusRunning Status = iota
	// StatusPaused: halted at a human-approval gate, awaiting Resume.
	StatusPaused
	// StatusCompleted: reached a terminal Done route.
	StatusCompleted
	// StatusFailed: a stage exhausted its retries or hit an unrecoverable error.
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusPaused:
		return "paused"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// PipelineState is the gob-serializable snapshot of a pipeline run, persisted by
// the CheckpointStore on every transition so the pipeline survives restarts.
//
// StageInput/StageOutput capture the boundary around the current stage so a
// paused run can either retry the stage (reject → re-run with StageInput) or
// proceed (approve → route using StageOutput).
//
// The durable conversation log is NOT part of this snapshot — it lives in the
// CheckpointStore's append-only history (CheckpointStore.History), keyed by
// PipelineID, so the FSM snapshot stays small while the lossless transcript
// grows independently. M4 compaction is a lossy projection sent to the model;
// the store's history is the source of truth.
//
// Note: Blackboard and StageInput/StageOutput hold arbitrary values; concrete
// types placed there must be gob-registerable (built-ins and common types are
// pre-registered in checkpoint.go; custom types need gob.Register by the caller).
type PipelineState struct {
	PipelineID   string
	CurrentStage string
	Status       Status
	RetryCount   map[string]int
	Blackboard   map[string]any
	StageInput   any
	StageOutput  any
	UpdatedAt    time.Time
}
