// Package memory is GoForge's long-term semantic-memory subsystem (M8): it
// indexes text by MEANING (embeddings + vector search) so an agent can recall
// relevant facts across runs and projects. It is distinct from M5's execution
// persistence (which restores a specific run's state by id); memory retrieves by
// similarity, regardless of which run produced the content.
//
// Ring placement: a Ring 2-level subsystem. It depends only on Ring 1 (pkg/llm,
// for llm.Message and the embedding client) and the pure-Go SQLite driver; it
// does NOT import pkg/agent. The auto-injection bridge (Source) returns the bare
// ContextSource function type so callers wire it into agent.ContextPolicy without
// this package depending on Ring 3.
//
// The interfaces are narrow and the backends concrete (the project's boundary
// convention): Embedder turns text into vectors, VectorStore does approximate
// nearest-neighbour over vectors, and Store composes the two into a text-level
// Retriever. The default VectorStore is SQLite-backed (persistent, cross-session,
// pure Go); an in-memory store serves tests and ephemeral use.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
)

// Document is one stored memory: its text, the vector it embeds to, a namespace
// (scope), and free-form metadata. ID is content-derived so re-adding identical
// text in the same namespace is an idempotent upsert (natural dedup).
type Document struct {
	ID        string
	Text      string
	Namespace string
	Metadata  map[string]string
	Vector    []float32
}

// Scored is a Document paired with its similarity to a query (1.0 = identical
// direction, 0 = orthogonal). Search returns these in descending Score order.
type Scored struct {
	Document Document
	Score    float32
}

// Embedder turns text into embedding vectors. Embed is batch (one call, many
// texts) mirroring Eino's EmbedStrings — cheaper than one call per text. All
// vectors from a given Embedder share Dimensions().
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
}

// VectorStore persists documents (with vectors) and does top-k cosine search
// within a namespace. It is the pluggable backend boundary: an in-memory store
// and a SQLite store implement it here; pgvector/qdrant/etc. can be added later
// without touching callers.
type VectorStore interface {
	// Add upserts documents by ID (idempotent for identical content).
	Add(ctx context.Context, docs []Document) error
	// Search returns the k documents in namespace most similar to vector,
	// highest score first. k <= 0 is treated as a small default.
	Search(ctx context.Context, namespace string, vector []float32, k int) ([]Scored, error)
	// Delete removes documents by ID (missing IDs are ignored).
	Delete(ctx context.Context, ids ...string) error
}

// Retriever is the text-level query surface an agent uses: embed the query and
// return the most similar stored documents. Store implements it.
type Retriever interface {
	Retrieve(ctx context.Context, namespace, query string, k int) ([]Scored, error)
}

// defaultTopK is used when a caller passes k <= 0.
const defaultTopK = 5

// Store composes an Embedder and a VectorStore into a text-level memory: Add
// embeds text then stores it; Retrieve embeds the query then searches. It is the
// high-level type the CLI/tools use.
type Store struct {
	emb Embedder
	vs  VectorStore
}

// NewStore builds a memory Store over an embedder and a vector backend.
func NewStore(emb Embedder, vs VectorStore) *Store { return &Store{emb: emb, vs: vs} }

var _ Retriever = (*Store)(nil)

// Add embeds text and stores it under namespace with optional metadata. It
// returns the (content-derived) document ID. Adding identical text to the same
// namespace is idempotent.
func (s *Store) Add(ctx context.Context, namespace, text string, metadata map[string]string) (string, error) {
	vecs, err := s.emb.Embed(ctx, []string{text})
	if err != nil {
		return "", fmt.Errorf("memory: embedding text: %w", err)
	}
	if len(vecs) != 1 {
		return "", fmt.Errorf("memory: embedder returned %d vectors for 1 text", len(vecs))
	}
	doc := Document{
		ID:        DocID(namespace, text),
		Text:      text,
		Namespace: namespace,
		Metadata:  metadata,
		Vector:    vecs[0],
	}
	if err := s.vs.Add(ctx, []Document{doc}); err != nil {
		return "", err
	}
	return doc.ID, nil
}

// Retrieve embeds query and returns the k most similar documents in namespace.
func (s *Store) Retrieve(ctx context.Context, namespace, query string, k int) ([]Scored, error) {
	vecs, err := s.emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("memory: embedding query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("memory: embedder returned %d vectors for 1 query", len(vecs))
	}
	return s.vs.Search(ctx, namespace, vecs[0], k)
}

// Delete removes documents by ID.
func (s *Store) Delete(ctx context.Context, ids ...string) error {
	return s.vs.Delete(ctx, ids...)
}

// DocID derives a stable document ID from namespace + text, so identical content
// in the same namespace maps to one entry (dedup on re-add).
func DocID(namespace, text string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + text))
	return hex.EncodeToString(sum[:16])
}

// cosine returns the cosine similarity of a and b, or 0 for mismatched or
// zero-magnitude vectors (so a bad entry never poisons a ranking).
func cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// topK sorts scored descending and truncates to k (k <= 0 ⇒ defaultTopK). It is
// a small insertion-free selection kept simple; N is modest (a namespace's docs).
func topK(scored []Scored, k int) []Scored {
	if k <= 0 {
		k = defaultTopK
	}
	// simple selection sort for the top k (stable enough; namespaces are small)
	for i := 0; i < len(scored) && i < k; i++ {
		max := i
		for j := i + 1; j < len(scored); j++ {
			if scored[j].Score > scored[max].Score {
				max = j
			}
		}
		scored[i], scored[max] = scored[max], scored[i]
	}
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored
}
