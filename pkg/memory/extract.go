package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

// FactKind distinguishes a timeless fact from a session episode.
type FactKind string

const (
	// KindFact is a durable truth about the project/code/decisions.
	KindFact FactKind = "fact"
	// KindEpisode is something that happened in a session worth remembering.
	KindEpisode FactKind = "episode"
)

// Fact is one distilled memory candidate extracted from a run transcript.
type Fact struct {
	Text   string   `json:"text"`
	Kind   FactKind `json:"kind"`
	Topics []string `json:"topics"`
}

// Extractor distills reusable facts/episodes from a completed run's transcript,
// so they can be stored and recalled in future, unrelated runs (M8-005).
type Extractor interface {
	Extract(ctx context.Context, transcript []llm.Message) ([]Fact, error)
}

// defaultMinMessages is the ShouldExtract gate: transcripts shorter than this
// rarely contain durable knowledge, so extraction is skipped (saves an LLM call).
const defaultMinMessages = 2

const extractSystemPrompt = `You extract durable, reusable knowledge from a software development session transcript.
Output ONLY a JSON array of objects: [{"text": string, "kind": "fact"|"episode", "topics": [string]}].
- "fact": a timeless truth about the project, code, architecture, or a decision — useful in FUTURE unrelated sessions.
- "episode": something that happened this session worth remembering (an outcome, a fix, a gotcha).
Keep each text one concise self-contained sentence. Omit ephemeral chatter, restated prompts, and anything not useful later.
If nothing is worth remembering, output [].`

// LLMExtractor is the default Extractor: it asks an LLM to distill the transcript
// into a JSON array of facts/episodes.
type LLMExtractor struct {
	llm     llm.LLM
	minMsgs int
}

var _ Extractor = (*LLMExtractor)(nil)

// NewLLMExtractor builds an LLM-backed extractor over the given client.
func NewLLMExtractor(client llm.LLM) *LLMExtractor {
	return &LLMExtractor{llm: client, minMsgs: defaultMinMessages}
}

// Extract renders the transcript and asks the LLM for durable facts/episodes.
// Returns nil (no extraction) for a too-short transcript.
func (e *LLMExtractor) Extract(ctx context.Context, transcript []llm.Message) ([]Fact, error) {
	if len(transcript) < e.minMsgs {
		return nil, nil
	}
	msgs := []llm.Message{
		llm.SystemMessage(extractSystemPrompt),
		llm.UserMessage("TRANSCRIPT:\n" + renderTranscript(transcript)),
	}
	resp, err := e.llm.Chat(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("memory: extraction chat: %w", err)
	}
	facts, err := parseFacts(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("memory: parsing extracted facts: %w", err)
	}
	return facts, nil
}

// ExtractInto runs an extractor over a transcript and stores each fact in the
// namespace, returning how many were added. Duplicates are skipped two ways:
// exact (content-hash IDs in Store.Add) and, when dedupScore > 0, semantic — a
// candidate whose nearest existing memory scores >= dedupScore is treated as
// already known. The Fact's kind/topics are recorded in metadata.
func ExtractInto(ctx context.Context, ex Extractor, store *Store, namespace string, transcript []llm.Message, dedupScore float32) (int, error) {
	facts, err := ex.Extract(ctx, transcript)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, f := range facts {
		text := strings.TrimSpace(f.Text)
		if text == "" {
			continue
		}
		if dedupScore > 0 {
			existing, err := store.Retrieve(ctx, namespace, text, 1)
			if err == nil && len(existing) > 0 && existing[0].Score >= dedupScore {
				continue // semantically already known
			}
		}
		meta := map[string]string{"kind": string(f.Kind)}
		if len(f.Topics) > 0 {
			meta["topics"] = strings.Join(f.Topics, ",")
		}
		if _, err := store.Add(ctx, namespace, text, meta); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// renderTranscript flattens a transcript into readable lines for the extractor
// prompt (skipping empty content; noting tool calls).
func renderTranscript(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Content != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "%s: (tool_call %s)\n", m.Role, tc.Name)
		}
	}
	return strings.TrimSpace(b.String())
}

// parseFacts extracts the JSON array from the LLM output (tolerating code fences
// or surrounding prose) and unmarshals it. An empty/absent array yields nil.
func parseFacts(s string) ([]Fact, error) {
	arr, ok := extractJSONArray(s)
	if !ok {
		return nil, fmt.Errorf("no JSON array in output")
	}
	var facts []Fact
	if err := json.Unmarshal([]byte(arr), &facts); err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return nil, nil
	}
	// Normalize kind: default unknown/empty to fact.
	for i := range facts {
		if facts[i].Kind != KindEpisode {
			facts[i].Kind = KindFact
		}
	}
	return facts, nil
}

// extractJSONArray returns the first balanced [...] substring, respecting string
// literals so brackets inside strings don't unbalance it.
func extractJSONArray(s string) (string, bool) {
	start := strings.IndexByte(s, '[')
	if start < 0 {
		return "", false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if inStr {
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
