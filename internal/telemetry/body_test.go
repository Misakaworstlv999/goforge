package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestParseBodyCapture(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want BodyCapture
		err  bool
	}{
		{"", BodyOff, false},
		{"preview", BodyPreview, false},
		{"full", BodyFull, false},
		{"bogus", BodyOff, true},
	} {
		got, err := ParseBodyCapture(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseBodyCapture(%q) want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseBodyCapture(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseBodyCapture(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestClipBody_preview(t *testing.T) {
	configureBodyCapture(BodyPreview, 10)
	got := clipBody("12345678901")
	if got != "1234567890…" {
		t.Fatalf("clipBody = %q", got)
	}
}

func TestRecordLLMExchange_offIsNoop(t *testing.T) {
	configureBodyCapture(BodyOff, 2048)
	sr := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	_, span := StartLLM(context.Background(), "m", 0)
	RecordLLMExchange(span, []llm.Message{llm.UserMessage("hi")}, llm.AssistantMessage("bye"))
	End(span, nil)
	if len(sr.Ended()[0].Attributes()) != 3 { // operation, model, step only
		t.Fatalf("attrs = %v", sr.Ended()[0].Attributes())
	}
}

func TestRecordLLMExchange_preview(t *testing.T) {
	configureBodyCapture(BodyPreview, 2048)
	sr := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	_, span := StartLLM(context.Background(), "m", 1)
	RecordLLMExchange(span,
		[]llm.Message{llm.UserMessage("hello")},
		llm.Message{Role: llm.RoleAssistant, Content: "world", ToolCalls: []llm.ToolCall{{Name: "read_file", Args: `{"path":"x"}`}}},
	)
	End(span, nil)
	prompt, ok := stringAttr(sr.Ended()[0], attrGenAIPrompt)
	if !ok || !strings.Contains(prompt, "hello") {
		t.Fatalf("prompt = %q ok=%v", prompt, ok)
	}
	completion, ok := stringAttr(sr.Ended()[0], attrGenAICompletion)
	if !ok || !strings.Contains(completion, "read_file") {
		t.Fatalf("completion = %q ok=%v", completion, ok)
	}
}

func stringAttr(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}
