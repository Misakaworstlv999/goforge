package llm

import "testing"

func TestApplyOptions_defaults(t *testing.T) {
	o := ApplyOptions(nil)
	if o.Model != "" {
		t.Errorf("default Model should be empty, got %q", o.Model)
	}
	if o.Temperature != nil {
		t.Errorf("default Temperature should be nil")
	}
	if o.MaxTokens != 0 {
		t.Errorf("default MaxTokens should be 0, got %d", o.MaxTokens)
	}
	if o.TopP != nil {
		t.Errorf("default TopP should be nil")
	}
	if o.StopSequences != nil {
		t.Errorf("default StopSequences should be nil")
	}
	if o.Tools != nil {
		t.Errorf("default Tools should be nil")
	}
}

func TestApplyOptions_single(t *testing.T) {
	tests := []struct {
		name  string
		opt   Option
		check func(t *testing.T, o Options)
	}{
		{
			name: "WithModel",
			opt:  WithModel("gpt-4o"),
			check: func(t *testing.T, o Options) {
				if o.Model != "gpt-4o" {
					t.Errorf("got %q, want %q", o.Model, "gpt-4o")
				}
			},
		},
		{
			name: "WithTemperature",
			opt:  WithTemperature(0.7),
			check: func(t *testing.T, o Options) {
				if o.Temperature == nil || *o.Temperature != 0.7 {
					t.Errorf("got %v, want 0.7", o.Temperature)
				}
			},
		},
		{
			name: "WithMaxTokens",
			opt:  WithMaxTokens(1024),
			check: func(t *testing.T, o Options) {
				if o.MaxTokens != 1024 {
					t.Errorf("got %d, want 1024", o.MaxTokens)
				}
			},
		},
		{
			name: "WithTopP",
			opt:  WithTopP(0.9),
			check: func(t *testing.T, o Options) {
				if o.TopP == nil || *o.TopP != 0.9 {
					t.Errorf("got %v, want 0.9", o.TopP)
				}
			},
		},
		{
			name: "WithStopSequences",
			opt:  WithStopSequences("stop1", "stop2"),
			check: func(t *testing.T, o Options) {
				if len(o.StopSequences) != 2 || o.StopSequences[0] != "stop1" {
					t.Errorf("got %v", o.StopSequences)
				}
			},
		},
		{
			name: "WithTools",
			opt:  WithTools(ToolSchema{Name: "calc"}),
			check: func(t *testing.T, o Options) {
				if len(o.Tools) != 1 || o.Tools[0].Name != "calc" {
					t.Errorf("got %v", o.Tools)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := ApplyOptions([]Option{tt.opt})
			tt.check(t, o)
		})
	}
}

func TestApplyOptions_override(t *testing.T) {
	o := ApplyOptions([]Option{
		WithModel("model-a"),
		WithMaxTokens(100),
		WithModel("model-b"),
		WithMaxTokens(200),
	})
	if o.Model != "model-b" {
		t.Errorf("last WithModel should win: got %q", o.Model)
	}
	if o.MaxTokens != 200 {
		t.Errorf("last WithMaxTokens should win: got %d", o.MaxTokens)
	}
}
