package llm

// Options holds all configurable parameters for an LLM call.
type Options struct {
	Model         string
	Temperature   *float64
	MaxTokens     int
	TopP          *float64
	StopSequences []string
	Tools         []ToolSchema
}

// Option is a functional option for configuring LLM calls.
type Option func(*Options)

// WithModel sets the model name (e.g., "gpt-4o", "claude-sonnet-4-20250514").
func WithModel(model string) Option {
	return func(o *Options) { o.Model = model }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) Option {
	return func(o *Options) { o.Temperature = &t }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(n int) Option {
	return func(o *Options) { o.MaxTokens = n }
}

// WithTopP sets the nucleus sampling parameter.
func WithTopP(p float64) Option {
	return func(o *Options) { o.TopP = &p }
}

// WithStopSequences sets sequences that cause the model to stop generating.
func WithStopSequences(seqs ...string) Option {
	return func(o *Options) { o.StopSequences = seqs }
}

// WithTools provides tool schemas for function calling.
func WithTools(tools ...ToolSchema) Option {
	return func(o *Options) { o.Tools = tools }
}

// ApplyOptions resolves a list of Option into an Options struct.
func ApplyOptions(opts []Option) Options {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
