package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestNoProvider_isNoop confirms the zero-value invariant: with the no-op
// provider (no Init), spans do not record, so instrumentation costs nothing.
func TestNoProvider_isNoop(t *testing.T) {
	otel.SetTracerProvider(noop.NewTracerProvider())
	_, span := StartStage(context.Background(), "x")
	if span.IsRecording() {
		t.Error("span should not be recording under the no-op provider")
	}
	span.End()
}

// TestSpans_recordedAndNested wires a recording provider and checks that the
// stage/LLM/tool helpers emit the expected spans, attributes, and parent nesting.
func TestSpans_recordedAndNested(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))

	ctx := context.Background()
	sctx, stage := StartStage(ctx, "build")
	_, llmSpan := StartLLM(sctx, "model-x", 0)
	RecordLLMUsage(llmSpan, 10, 3)
	End(llmSpan, nil)
	// The agent opens the tool span from the step/stage context (not the LLM
	// context), so it nests as a sibling of the LLM span under the stage.
	_, toolSpan := StartTool(sctx, 0, 2, "read_file,write_file")
	End(toolSpan, nil)
	End(stage, nil)

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(spans))
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byName[s.Name()] = s
	}

	st, ok := byName["pipeline.stage build"]
	if !ok {
		t.Fatal("missing stage span")
	}
	chat, ok := byName["gen_ai.chat"]
	if !ok {
		t.Fatal("missing llm span")
	}
	tl, ok := byName["execute_tool"]
	if !ok {
		t.Fatal("missing tool span")
	}

	if got, ok := intAttr(chat, attrInputTokens); !ok || got != 10 {
		t.Errorf("llm input tokens = %d (ok=%v), want 10", got, ok)
	}
	if got, ok := intAttr(chat, attrOutputTokens); !ok || got != 3 {
		t.Errorf("llm output tokens = %d (ok=%v), want 3", got, ok)
	}

	// LLM and tool spans must nest under the stage span.
	stageID := st.SpanContext().SpanID()
	if chat.Parent().SpanID() != stageID {
		t.Error("llm span is not nested under the stage span")
	}
	if tl.Parent().SpanID() != stageID {
		t.Error("tool span is not nested under the stage span")
	}
}

func TestInit_emptyEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Init(empty) err = %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown err = %v", err)
	}
}

func intAttr(s sdktrace.ReadOnlySpan, key string) (int64, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsInt64(), true
		}
	}
	return 0, false
}
