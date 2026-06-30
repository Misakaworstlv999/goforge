package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Options configures OTLP export. A zero Options (empty Endpoint) keeps telemetry
// a no-op.
type Options struct {
	// Endpoint is the OTLP/HTTP collector endpoint (host:port). Empty ⇒ telemetry
	// stays a no-op (nothing is exported). It must come from flag/env — never
	// hardcode an endpoint.
	Endpoint string
	// Insecure sends OTLP over plaintext HTTP (no TLS).
	Insecure bool
	// ServiceName is the service.name resource attribute.
	ServiceName string
}

// Init wires OTLP trace + metric exporters into the global OTel providers and
// returns a shutdown func that flushes and releases them. With an empty
// opts.Endpoint it is a deliberate no-op: nothing is exported, the global no-op
// providers stay in place (zero overhead), and the returned shutdown is a no-op.
// This is what keeps "telemetry unconfigured" identical to today's behavior.
func Init(ctx context.Context, opts Options) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if opts.Endpoint == "" {
		return noop, nil
	}

	svc := opts.ServiceName
	if svc == "" {
		svc = "goforge"
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", svc)))
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(opts.Endpoint)}
	metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(opts.Endpoint)}
	if opts.Insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}

	texp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(texp),
	)
	otel.SetTracerProvider(tp)

	mexp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)),
	)
	otel.SetMeterProvider(mp)

	return func(c context.Context) error {
		errT := tp.Shutdown(c)
		errM := mp.Shutdown(c)
		if errT != nil {
			return errT
		}
		return errM
	}, nil
}
