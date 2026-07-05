package memory

import (
	"context"
	"iter"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// cannedLLM returns a fixed assistant reply, for extractor tests without network.
type cannedLLM struct{ reply string }

func (c cannedLLM) Chat(context.Context, []llm.Message, ...llm.Option) (*llm.Response, error) {
	return &llm.Response{Message: llm.AssistantMessage(c.reply), StopReason: llm.StopReasonEnd}, nil
}

func (c cannedLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		yield(llm.Chunk{Delta: c.reply, StopReason: llm.StopReasonEnd}, nil)
	}
}

func transcript() []llm.Message {
	return []llm.Message{
		llm.UserMessage("set up the build"),
		llm.AssistantMessage("done; note the build uses go 1.25 and deploys run on fridays"),
	}
}

func TestExtractInto_storesAndDedups(t *testing.T) {
	// LLM returns two facts, fenced + with prose (exercises tolerant parsing).
	reply := "Sure, here they are:\n```json\n" +
		`[{"text":"the build uses go 1.25","kind":"fact","topics":["build"]},` +
		`{"text":"deploys run on fridays","kind":"episode","topics":["deploy"]}]` +
		"\n```"
	ex := NewLLMExtractor(cannedLLM{reply: reply})
	store := NewStore(fakeEmbedder{keywords: []string{"build", "deploy", "go"}}, NewMemStore())
	ctx := context.Background()

	added, err := ExtractInto(ctx, ex, store, "proj", transcript(), 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	got, err := store.Retrieve(ctx, "proj", "how does the build work", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("stored facts not retrievable")
	}

	// Re-extracting identical facts must dedup (semantic score >= 0.95).
	again, err := ExtractInto(ctx, ex, store, "proj", transcript(), 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-extract added %d, want 0 (dedup)", again)
	}
}

func TestExtract_shortTranscriptSkipped(t *testing.T) {
	ex := NewLLMExtractor(cannedLLM{reply: `[{"text":"x","kind":"fact"}]`})
	facts, err := ex.Extract(context.Background(), []llm.Message{llm.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if facts != nil {
		t.Errorf("short transcript should skip extraction, got %v", facts)
	}
}

func TestParseFacts(t *testing.T) {
	facts, err := parseFacts("noise [] more noise")
	if err != nil || facts != nil {
		t.Errorf("empty array = %v (err %v), want nil", facts, err)
	}
	facts, err = parseFacts(`[{"text":"a","kind":"weird"},{"text":"b","kind":"episode"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].Kind != KindFact || facts[1].Kind != KindEpisode {
		t.Errorf("kind normalization wrong: %+v", facts)
	}
	if _, err := parseFacts("no json here"); err == nil {
		t.Error("expected error when no array present")
	}
}
