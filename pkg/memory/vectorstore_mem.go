package memory

import (
	"context"
	"sync"
)

// MemStore is a pure-Go, in-memory VectorStore doing brute-force cosine search.
// It is the default for tests and ephemeral use (nothing survives process exit).
// For cross-session memory use OpenSQLiteStore instead.
type MemStore struct {
	mu   sync.RWMutex
	docs map[string]Document // keyed by Document.ID
}

var _ VectorStore = (*MemStore)(nil)

// NewMemStore returns an empty in-memory vector store.
func NewMemStore() *MemStore { return &MemStore{docs: make(map[string]Document)} }

// Add upserts documents by ID. A defensive copy of each vector is stored so a
// caller mutating its slice afterwards cannot corrupt the index.
func (m *MemStore) Add(_ context.Context, docs []Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range docs {
		vec := make([]float32, len(d.Vector))
		copy(vec, d.Vector)
		d.Vector = vec
		m.docs[d.ID] = d
	}
	return nil
}

// Search returns the top-k documents in namespace by cosine similarity.
func (m *MemStore) Search(_ context.Context, namespace string, vector []float32, k int) ([]Scored, error) {
	m.mu.RLock()
	var scored []Scored
	for _, d := range m.docs {
		if d.Namespace != namespace {
			continue
		}
		scored = append(scored, Scored{Document: d, Score: cosine(vector, d.Vector)})
	}
	m.mu.RUnlock()
	return topK(scored, k), nil
}

// Delete removes documents by ID (missing IDs ignored).
func (m *MemStore) Delete(_ context.Context, ids ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.docs, id)
	}
	return nil
}
