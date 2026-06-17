package agent

import "github.com/Misakaworstlv999/goforge/pkg/llm"

// TokenCounter estimates the token footprint of a message slice. It is a narrow
// interface so a precise tokenizer (e.g. tiktoken) can replace the default
// heuristic later without touching callers.
//
// Note: this counts message *content* only. The authoritative budget signal at
// runtime is the provider-reported llm.Usage from each Chat response (see
// SimpleAgent.Run); the counter is the fallback before the first response and
// the basis for offline tests.
type TokenCounter interface {
	Count(messages []llm.Message) int
}

const (
	defaultRunesPerToken = 4.0 // ~4 UTF-8 runes/token for English; tune down for CJK
	perMessageOverhead   = 4   // role + delimiters per message
	perToolCallOverhead  = 4   // structural overhead per tool call
)

// Estimator is the default heuristic TokenCounter. RunesPerToken is configurable
// because the ratio is language-dependent: ~4 for English, closer to ~2 for
// Chinese. A fixed 4 badly under-counts CJK-heavy content.
type Estimator struct {
	RunesPerToken float64
}

// EstimatorOption configures an Estimator.
type EstimatorOption func(*Estimator)

// WithRunesPerToken overrides the runes-per-token ratio (must be > 0).
func WithRunesPerToken(r float64) EstimatorOption {
	return func(e *Estimator) {
		if r > 0 {
			e.RunesPerToken = r
		}
	}
}

// NewEstimator returns an Estimator with the default ratio unless overridden.
func NewEstimator(opts ...EstimatorOption) *Estimator {
	e := &Estimator{RunesPerToken: defaultRunesPerToken}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Count estimates the total tokens for all messages.
func (e *Estimator) Count(messages []llm.Message) int {
	ratio := e.RunesPerToken
	if ratio <= 0 {
		ratio = defaultRunesPerToken
	}

	total := 0
	for _, m := range messages {
		total += perMessageOverhead
		total += runesToTokens(m.Content, ratio)
		for _, tc := range m.ToolCalls {
			total += perToolCallOverhead
			total += runesToTokens(tc.Name, ratio)
			total += runesToTokens(tc.Args, ratio)
		}
	}
	return total
}

// runesToTokens converts a string's rune length to an estimated token count,
// rounding up so any non-empty string costs at least one token.
func runesToTokens(s string, ratio float64) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	return max(int(float64(n)/ratio+0.999), 1)
}
