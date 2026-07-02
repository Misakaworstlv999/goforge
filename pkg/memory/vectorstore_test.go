package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// bothStores runs a test body against each VectorStore implementation, so the
// in-memory and SQLite backends are held to the identical contract.
func bothStores(t *testing.T, body func(t *testing.T, vs VectorStore)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) { body(t, NewMemStore()) })
	t.Run("sqlite", func(t *testing.T) {
		s, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "mem.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		body(t, s)
	})
}

func doc(id, ns, text string, vec ...float32) Document {
	return Document{ID: id, Namespace: ns, Text: text, Vector: vec}
}

func TestVectorStore_searchRanksByCosine(t *testing.T) {
	bothStores(t, func(t *testing.T, vs VectorStore) {
		ctx := context.Background()
		must(t, vs.Add(ctx, []Document{
			doc("a", "proj", "east", 1, 0),
			doc("b", "proj", "north", 0, 1),
			doc("c", "proj", "east-ish", 0.9, 0.1),
		}))
		got, err := vs.Search(ctx, "proj", []float32{1, 0}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d results, want 2", len(got))
		}
		// Nearest to (1,0) is "a" (identical), then "c".
		if got[0].Document.ID != "a" || got[1].Document.ID != "c" {
			t.Errorf("ranking = %s,%s; want a,c", got[0].Document.ID, got[1].Document.ID)
		}
		if got[0].Score < got[1].Score {
			t.Errorf("scores not descending: %v then %v", got[0].Score, got[1].Score)
		}
	})
}

func TestVectorStore_namespaceIsolation(t *testing.T) {
	bothStores(t, func(t *testing.T, vs VectorStore) {
		ctx := context.Background()
		must(t, vs.Add(ctx, []Document{
			doc("a", "proj-1", "x", 1, 0),
			doc("b", "proj-2", "y", 1, 0),
		}))
		got, err := vs.Search(ctx, "proj-1", []float32{1, 0}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Document.ID != "a" {
			t.Errorf("namespace isolation broken: got %+v", got)
		}
	})
}

func TestVectorStore_upsertAndDelete(t *testing.T) {
	bothStores(t, func(t *testing.T, vs VectorStore) {
		ctx := context.Background()
		must(t, vs.Add(ctx, []Document{doc("a", "p", "old", 1, 0)}))
		// Upsert same ID with new text/metadata.
		d := doc("a", "p", "new", 1, 0)
		d.Metadata = map[string]string{"k": "v"}
		must(t, vs.Add(ctx, []Document{d}))

		got, err := vs.Search(ctx, "p", []float32{1, 0}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Document.Text != "new" || got[0].Document.Metadata["k"] != "v" {
			t.Errorf("upsert wrong: %+v", got)
		}

		must(t, vs.Delete(ctx, "a"))
		got, err = vs.Search(ctx, "p", []float32{1, 0}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("delete failed: %d docs remain", len(got))
		}
	})
}

func TestCosine(t *testing.T) {
	if s := cosine([]float32{1, 0}, []float32{1, 0}); s < 0.999 {
		t.Errorf("identical vectors cosine = %v, want ~1", s)
	}
	if s := cosine([]float32{1, 0}, []float32{0, 1}); s > 0.001 || s < -0.001 {
		t.Errorf("orthogonal cosine = %v, want ~0", s)
	}
	if s := cosine([]float32{1, 0}, []float32{1}); s != 0 {
		t.Errorf("mismatched-length cosine = %v, want 0", s)
	}
	if s := cosine([]float32{0, 0}, []float32{1, 0}); s != 0 {
		t.Errorf("zero-magnitude cosine = %v, want 0", s)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
