package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetrics_recorded(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	bindMetrics()

	ctx := context.Background()
	RecordLLMMetrics(ctx, "model-x", 10, 4, 50*time.Millisecond)
	RecordStageMetrics(ctx, "coding", 120*time.Millisecond)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = true
		}
	}
	for _, want := range []string{metricTokenUsage, metricOpDuration, metricRequestCount, metricStageDuration} {
		if !got[want] {
			t.Errorf("metric %q not recorded; got %v", want, got)
		}
	}
}

// TestRecordMetrics_safeWithoutRealProvider ensures the Record* helpers never
// panic when instruments are unbound / no-op (the zero-value / pre-Init state).
func TestRecordMetrics_safeWithoutRealProvider(t *testing.T) {
	RecordLLMMetrics(context.Background(), "m", 0, 0, time.Millisecond)
	RecordStageMetrics(context.Background(), "s", time.Millisecond)
}
