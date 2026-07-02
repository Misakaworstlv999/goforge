package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIEmbedder_batch mocks an OpenAI-compatible embeddings endpoint and
// checks batch embedding: one vector per input, placed by Index, and Dimensions
// learned from the response.
func TestOpenAIEmbedder_batch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Return vectors out of order (index 1 before 0) to test reordering.
		resp := map[string]any{
			"object": "list",
			"model":  "text-embedding-3-small",
			"data": []map[string]any{
				{"object": "embedding", "index": 1, "embedding": []float64{0, 1}},
				{"object": "embedding", "index": 0, "embedding": []float64{1, 0}},
			},
			"usage": map[string]any{"prompt_tokens": 2, "total_tokens": 2},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(EmbedderConfig{APIKey: "test", BaseURL: srv.URL + "/v1"})
	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	// index 0 → (1,0), index 1 → (0,1) despite response ordering.
	if vecs[0][0] != 1 || vecs[0][1] != 0 || vecs[1][0] != 0 || vecs[1][1] != 1 {
		t.Errorf("vectors misordered: %v", vecs)
	}
	if e.Dimensions() != 2 {
		t.Errorf("Dimensions = %d, want 2", e.Dimensions())
	}
}

// fakeEmbedder is a deterministic, network-free Embedder for Store tests: each
// keyword maps to a dimension, so texts sharing keywords embed near each other.
type fakeEmbedder struct{ keywords []string }

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(f.keywords))
		low := strings.ToLower(t)
		for j, kw := range f.keywords {
			if strings.Contains(low, kw) {
				v[j] = 1
			}
		}
		out[i] = v
	}
	return out, nil
}

func (f fakeEmbedder) Dimensions() int { return len(f.keywords) }

func TestStore_addRetrieve(t *testing.T) {
	emb := fakeEmbedder{keywords: []string{"cat", "dog", "car"}}
	store := NewStore(emb, NewMemStore())
	ctx := context.Background()

	if _, err := store.Add(ctx, "proj", "cats are cute felines", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(ctx, "proj", "dogs bark loudly", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(ctx, "proj", "the car is fast", map[string]string{"kind": "fact"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Retrieve(ctx, "proj", "my cat purrs", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Document.Text, "cats") {
		t.Fatalf("expected the cat memory, got %+v", got)
	}
}

func TestStore_addIsIdempotent(t *testing.T) {
	emb := fakeEmbedder{keywords: []string{"x"}}
	store := NewStore(emb, NewMemStore())
	ctx := context.Background()

	id1, err := store.Add(ctx, "p", "same text", nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.Add(ctx, "p", "same text", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("content-derived IDs differ: %s vs %s", id1, id2)
	}
	got, err := store.Retrieve(ctx, "p", "same text", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("re-adding identical text should dedup, got %d docs", len(got))
	}
}
