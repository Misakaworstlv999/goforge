// Package telemetry is the cross-cutting OpenTelemetry seam (M7-003). It exposes
// a handful of span helpers that the pipeline engine and agent loop call at their
// safe points (stage, LLM call, tool batch). Until Init wires a real SDK provider
// (see setup.go), every helper resolves to the OTel global no-op provider, so
// instrumentation costs effectively nothing and changes no behavior — the
// zero-value invariant the rest of M7 maintains.
//
// Only the inner rings that own a loop import this package (pkg/pipeline,
// pkg/agent); Ring 1 (llm) and Ring 2 (tool) stay telemetry-free, so spans nest
// stage → llm/tool via the context returned by Start*.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// scopeName identifies this instrumentation scope in emitted spans.
const scopeName = "github.com/Misakaworstlv999/goforge"

// Attribute keys. The gen_ai.* keys follow the OpenTelemetry GenAI semantic
// conventions; they are kept as local consts to avoid pinning a semconv module
// version. The goforge.* keys are engine-specific.
const (
	attrOperation    = "gen_ai.operation.name"
	attrRequestModel = "gen_ai.request.model"
	attrInputTokens  = "gen_ai.usage.input_tokens"
	attrOutputTokens = "gen_ai.usage.output_tokens"
	attrToolName     = "gen_ai.tool.name"
	attrStage        = "goforge.pipeline.stage"
	attrStep         = "goforge.agent.step"
	attrToolCount    = "goforge.tool.count"
)

// tracer returns the process tracer from the globally-registered provider. Until
// Init wires a real SDK provider this is the OTel no-op tracer (zero cost).
func tracer() trace.Tracer { return otel.Tracer(scopeName) }

// StartStage opens a span covering one pipeline stage execution. Pass the
// returned context into the stage so its LLM/tool spans nest beneath it.
func StartStage(ctx context.Context, stage string) (context.Context, trace.Span) {
	return tracer().Start(ctx, "pipeline.stage "+stage,
		trace.WithAttributes(attribute.String(attrStage, stage)))
}

// StartLLM opens a span around a single model call at ReAct step.
func StartLLM(ctx context.Context, model string, step int) (context.Context, trace.Span) {
	return tracer().Start(ctx, "gen_ai.chat", trace.WithAttributes(
		attribute.String(attrOperation, "chat"),
		attribute.String(attrRequestModel, model),
		attribute.Int(attrStep, step),
	))
}

// RecordLLMUsage annotates an LLM span with provider-reported token usage. It is
// a no-op when the span is not recording (so it costs nothing under the no-op
// provider).
func RecordLLMUsage(span trace.Span, promptTokens, completionTokens int) {
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.Int(attrInputTokens, promptTokens),
		attribute.Int(attrOutputTokens, completionTokens),
	)
}

// StartTool opens a span around a batch of tool executions at ReAct step.
func StartTool(ctx context.Context, step, count int, names string) (context.Context, trace.Span) {
	return tracer().Start(ctx, "execute_tool", trace.WithAttributes(
		attribute.String(attrOperation, "execute_tool"),
		attribute.Int(attrStep, step),
		attribute.Int(attrToolCount, count),
		attribute.String(attrToolName, names),
	))
}

// End finishes a span, recording err as the span status when non-nil. Pairs with
// any Start* helper: `ctx, span := telemetry.StartX(...); defer telemetry.End(span, err)`
// is not usable directly (err is set later), so callers typically call End
// explicitly after the instrumented operation.
func End(span trace.Span, err error) {
	if err != nil && span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
