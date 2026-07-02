package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metric instrument + attribute names (GenAI semantic conventions where they
// apply; goforge.* for engine-specific ones).
const (
	metricTokenUsage    = "gen_ai.client.token.usage"
	metricOpDuration    = "gen_ai.client.operation.duration"
	metricRequestCount  = "gen_ai.client.request.count"
	metricStageDuration = "goforge.pipeline.stage.duration"
	attrTokenType       = "gen_ai.token.type"
)

// Instruments, bound to the global meter provider by bindMetrics (called from
// Init). They stay nil until bound; every Record* call nil-guards, so metrics
// are a no-op before Init or if the no-op provider is active (zero overhead).
var (
	mTokenUsage    metric.Int64Histogram
	mOpDuration    metric.Float64Histogram
	mRequestCount  metric.Int64Counter
	mStageDuration metric.Float64Histogram
)

// bindMetrics (re)creates the instruments from the current global meter provider.
// Called at the end of Init in both paths: under the no-op provider it yields
// no-op instruments; once Init has set a real provider they export via OTLP.
// Instrument-creation errors are non-fatal — the instrument stays nil and its
// Record* calls are skipped, so telemetry never breaks the run.
func bindMetrics() {
	m := otel.Meter(scopeName)
	mTokenUsage, _ = m.Int64Histogram(metricTokenUsage,
		metric.WithDescription("LLM token usage per call, split by token type"),
		metric.WithUnit("{token}"))
	mOpDuration, _ = m.Float64Histogram(metricOpDuration,
		metric.WithDescription("LLM call latency"),
		metric.WithUnit("s"))
	mRequestCount, _ = m.Int64Counter(metricRequestCount,
		metric.WithDescription("LLM call count"),
		metric.WithUnit("{request}"))
	mStageDuration, _ = m.Float64Histogram(metricStageDuration,
		metric.WithDescription("Pipeline stage execution time"),
		metric.WithUnit("s"))
}

// RecordLLMMetrics records one LLM call's token usage, latency, and count. Safe
// to call always: a no-op when instruments are unbound (before Init / no-op
// provider). promptTokens/completionTokens may be 0 (e.g. on a failed call).
func RecordLLMMetrics(ctx context.Context, model string, promptTokens, completionTokens int, dur time.Duration) {
	modelAttr := attribute.String(attrRequestModel, model)
	if mRequestCount != nil {
		mRequestCount.Add(ctx, 1, metric.WithAttributes(modelAttr))
	}
	if mOpDuration != nil {
		mOpDuration.Record(ctx, dur.Seconds(), metric.WithAttributes(modelAttr))
	}
	if mTokenUsage != nil {
		if promptTokens > 0 {
			mTokenUsage.Record(ctx, int64(promptTokens),
				metric.WithAttributes(modelAttr, attribute.String(attrTokenType, "input")))
		}
		if completionTokens > 0 {
			mTokenUsage.Record(ctx, int64(completionTokens),
				metric.WithAttributes(modelAttr, attribute.String(attrTokenType, "output")))
		}
	}
}

// RecordStageMetrics records one pipeline stage's execution time. No-op when
// unbound.
func RecordStageMetrics(ctx context.Context, stage string, dur time.Duration) {
	if mStageDuration != nil {
		mStageDuration.Record(ctx, dur.Seconds(), metric.WithAttributes(attribute.String(attrStage, stage)))
	}
}
