package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSource_injectsRelevantMemory(t *testing.T) {
	emb := fakeEmbedder{keywords: []string{"cat", "dog"}}
	store := NewStore(emb, NewMemStore())
	ctx := context.Background()
	if _, err := store.Add(ctx, "proj", "the cat likes tuna", nil); err != nil {
		t.Fatal(err)
	}

	src := Source(store, "proj", 3)
	msgs, err := src(ctx, "tell me about the cat")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "the cat likes tuna") {
		t.Fatalf("source did not inject the memory: %+v", msgs)
	}
}

func TestSource_emptyInjectsNothing(t *testing.T) {
	store := NewStore(fakeEmbedder{keywords: []string{"x"}}, NewMemStore())
	msgs, err := Source(store, "proj", 3)(context.Background(), "anything")
	if err != nil || msgs != nil {
		t.Errorf("empty memory should inject nothing: msgs=%v err=%v", msgs, err)
	}
}

// errRetriever always fails, to check Source's best-effort contract.
type errRetriever struct{}

func (errRetriever) Retrieve(context.Context, string, string, int) ([]Scored, error) {
	return nil, errors.New("embeddings down")
}

func TestSource_bestEffortOnError(t *testing.T) {
	msgs, err := Source(errRetriever{}, "proj", 3)(context.Background(), "q")
	if err != nil || msgs != nil {
		t.Errorf("retrieval error must be swallowed (auxiliary recall): msgs=%v err=%v", msgs, err)
	}
}
