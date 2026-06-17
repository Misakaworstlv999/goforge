package agent

import (
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestEstimator_Count(t *testing.T) {
	est := NewEstimator()

	tests := []struct {
		name     string
		messages []llm.Message
		wantZero bool // true ⇒ expect 0; false ⇒ expect > 0
	}{
		{"empty slice", nil, true},
		{"plain text", []llm.Message{llm.UserMessage("hello world")}, false},
		{"tool call", []llm.Message{{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "calculator", Args: `{"a":1}`}},
		}}, false},
		{"tool result", []llm.Message{llm.ToolMessage("c1", "42")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := est.Count(tt.messages)
			if tt.wantZero && got != 0 {
				t.Errorf("Count = %d, want 0", got)
			}
			if !tt.wantZero && got <= 0 {
				t.Errorf("Count = %d, want > 0", got)
			}
		})
	}
}

func TestEstimator_monotonic(t *testing.T) {
	est := NewEstimator()
	one := []llm.Message{llm.UserMessage("some text here")}
	two := append([]llm.Message{}, one...)
	two = append(two, llm.AssistantMessage("a reply with more content"))

	if est.Count(two) <= est.Count(one) {
		t.Errorf("adding a message must increase the count: one=%d two=%d", est.Count(one), est.Count(two))
	}
}

func TestEstimator_runesPerTokenAffectsCount(t *testing.T) {
	msgs := []llm.Message{llm.UserMessage("这是一段中文文本用于测试分词比例")}

	coarse := NewEstimator(WithRunesPerToken(4)).Count(msgs) // English-tuned
	fine := NewEstimator(WithRunesPerToken(2)).Count(msgs)   // CJK-tuned

	// A smaller runes/token ratio yields MORE tokens for the same text.
	if fine <= coarse {
		t.Errorf("CJK ratio should count more tokens: ratio4=%d ratio2=%d", coarse, fine)
	}
}

func TestEstimator_invalidRatioFallsBack(t *testing.T) {
	// Non-positive override is ignored; counter still works.
	est := NewEstimator(WithRunesPerToken(-1))
	if est.RunesPerToken != defaultRunesPerToken {
		t.Errorf("RunesPerToken = %v, want default %v", est.RunesPerToken, defaultRunesPerToken)
	}
	if est.Count([]llm.Message{llm.UserMessage("x")}) <= 0 {
		t.Error("count should be positive")
	}
}

// compile-time: Estimator satisfies TokenCounter.
var _ TokenCounter = (*Estimator)(nil)
