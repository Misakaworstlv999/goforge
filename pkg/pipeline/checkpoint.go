package pipeline

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func init() {
	// Register common concrete types so gob can encode them inside the
	// any-typed Blackboard / StageInput / StageOutput fields. Custom types
	// placed on the blackboard must be registered by the caller via gob.Register.
	gob.Register("")
	gob.Register(int(0))
	gob.Register(float64(0))
	gob.Register(false)
	gob.Register([]any{})
	gob.Register(map[string]any{})
	gob.Register([]string{})
	gob.Register([]llm.Message{})
	gob.Register(llm.Message{})
}

// ErrNotFound is returned by Load when no checkpoint exists for a pipeline ID.
var ErrNotFound = errors.New("pipeline: checkpoint not found")

// Audit action constants used in AuditEntry.Action.
const (
	ActionEnter     = "enter"
	ActionGatePass  = "gate_pass"
	ActionGateFail  = "gate_fail"
	ActionRetry     = "retry"
	ActionInterrupt = "interrupt"
	ActionResume    = "resume"
	ActionComplete  = "complete"
	ActionFail      = "fail"
)

// AuditEntry records one pipeline transition for the audit log (M5-005).
type AuditEntry struct {
	Timestamp  time.Time
	PipelineID string
	Stage      string
	Action     string
	Detail     string
}

// PipelineInfo is a summary row returned by List.
type PipelineInfo struct {
	PipelineID   string
	CurrentStage string
	Status       Status
	UpdatedAt    time.Time
}

// CheckpointStore persists pipeline state, the durable (lossless) conversation
// log, and the audit trail. MemoryStore and SQLiteStore both satisfy it.
type CheckpointStore interface {
	// Save persists a snapshot of the pipeline state (overwriting any prior one
	// for the same PipelineID).
	Save(ctx context.Context, st *PipelineState) error
	// Load returns the persisted state, or ErrNotFound.
	Load(ctx context.Context, pipelineID string) (*PipelineState, error)
	// List summarizes all persisted pipelines.
	List(ctx context.Context) ([]PipelineInfo, error)
	// AppendHistory appends messages to the durable conversation log as they are
	// produced (append-on-produce), keeping the lossless full transcript behind
	// M4's lossy projection. This is the source of truth for history.
	AppendHistory(ctx context.Context, pipelineID string, msgs []llm.Message) error
	// History returns the full durable transcript for a pipeline, in order.
	History(ctx context.Context, pipelineID string) ([]llm.Message, error)
	// Audit appends an audit entry.
	Audit(ctx context.Context, entry AuditEntry) error
	// AuditLog returns the audit entries for a pipeline in insertion order.
	AuditLog(ctx context.Context, pipelineID string) ([]AuditEntry, error)
}

// gobClone serializes and reloads a PipelineState. It both decouples the stored
// copy from the caller's and surfaces any gob-encodability problem at Save time.
func gobClone(st *PipelineState) (*PipelineState, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		return nil, fmt.Errorf("encoding checkpoint: %w", err)
	}
	var out PipelineState
	if err := gob.NewDecoder(&buf).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding checkpoint: %w", err)
	}
	return &out, nil
}

// MemoryStore is an in-memory CheckpointStore for tests and ephemeral runs. It
// stores gob-cloned snapshots so its serialization behavior matches SQLiteStore.
type MemoryStore struct {
	mu      sync.RWMutex
	states  map[string]*PipelineState
	history map[string][]llm.Message
	audit   map[string][]AuditEntry
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		states:  make(map[string]*PipelineState),
		history: make(map[string][]llm.Message),
		audit:   make(map[string][]AuditEntry),
	}
}

var _ CheckpointStore = (*MemoryStore)(nil)

func (m *MemoryStore) Save(_ context.Context, st *PipelineState) error {
	clone, err := gobClone(st)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[st.PipelineID] = clone
	return nil
}

func (m *MemoryStore) Load(_ context.Context, pipelineID string) (*PipelineState, error) {
	m.mu.RLock()
	st, ok := m.states[pipelineID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return gobClone(st)
}

func (m *MemoryStore) List(_ context.Context) ([]PipelineInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PipelineInfo, 0, len(m.states))
	for _, st := range m.states {
		out = append(out, PipelineInfo{
			PipelineID:   st.PipelineID,
			CurrentStage: st.CurrentStage,
			Status:       st.Status,
			UpdatedAt:    st.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PipelineID < out[j].PipelineID })
	return out, nil
}

func (m *MemoryStore) AppendHistory(_ context.Context, pipelineID string, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history[pipelineID] = append(m.history[pipelineID], msgs...)
	return nil
}

// History returns the full durable transcript appended for a pipeline, in order.
func (m *MemoryStore) History(_ context.Context, pipelineID string) ([]llm.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]llm.Message, len(m.history[pipelineID]))
	copy(out, m.history[pipelineID])
	return out, nil
}

func (m *MemoryStore) Audit(_ context.Context, entry AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit[entry.PipelineID] = append(m.audit[entry.PipelineID], entry)
	return nil
}

func (m *MemoryStore) AuditLog(_ context.Context, pipelineID string) ([]AuditEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]AuditEntry, len(m.audit[pipelineID]))
	copy(out, m.audit[pipelineID])
	return out, nil
}
