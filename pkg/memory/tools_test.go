package memory

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryTools_addThenSearch(t *testing.T) {
	emb := fakeEmbedder{keywords: []string{"deploy", "test"}}
	store := NewStore(emb, NewMemStore())
	tools := MemoryTools(store, "proj")

	byName := map[string]int{}
	for i, tl := range tools {
		byName[tl.Name()] = i
	}
	add, ok1 := byName["memory_add"]
	search, ok2 := byName["memory_search"]
	if !ok1 || !ok2 {
		t.Fatalf("expected memory_add + memory_search, got %v", byName)
	}
	ctx := context.Background()

	if out, err := tools[add].Execute(ctx, []byte(`{"text":"deploy runs on fridays"}`)); err != nil || !strings.Contains(out, "remembered") {
		t.Fatalf("memory_add = %q (err %v)", out, err)
	}
	out, err := tools[search].Execute(ctx, []byte(`{"query":"when do we deploy","k":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deploy runs on fridays") {
		t.Errorf("memory_search did not recall the fact: %q", out)
	}
}

func TestMemoryTools_searchEmpty(t *testing.T) {
	store := NewStore(fakeEmbedder{keywords: []string{"x"}}, NewMemStore())
	tools := MemoryTools(store, "proj")
	for _, tl := range tools {
		if tl.Name() == "memory_search" {
			out, err := tl.Execute(context.Background(), []byte(`{"query":"nothing stored"}`))
			if err != nil || !strings.Contains(out, "no relevant memory") {
				t.Errorf("empty search = %q (err %v)", out, err)
			}
		}
	}
}
