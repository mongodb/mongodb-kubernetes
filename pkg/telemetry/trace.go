package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var TRACER = otel.Tracer("evergreen-agent")

// SetupTracing initializes OpenTelemetry tracing from environment variables
func SetupTracing(ctx context.Context, traceIDHex, parentIDHex, endpoint string) (context.Context, error) {
	if traceIDHex == "" || parentIDHex == "" || endpoint == "" {
		Logger.Info("tracing environment variables missing, not configuring tracing")
		return ctx, nil
	}

	traceID, err := trace.TraceIDFromHex(traceIDHex)
	if err != nil {
		return ctx, err
	}
	spanID, err := trace.SpanIDFromHex(parentIDHex)
	if err != nil {
		return ctx, err
	}

	// Create a span context with trace flags set
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     false,
	})

	// Create a non-recording span with the span context
	ctxWithSpan := trace.ContextWithSpanContext(ctx, sc)

	// Create a span processor with OTLP exporter
	exporter, err := otlptracegrpc.New(
		context.Background(),
		otlptracegrpc.WithEndpoint(endpoint),
	)
	if err != nil {
		return ctx, err
	}

	// Create a batch span processor
	bsp := sdktrace.NewBatchSpanProcessor(exporter)

	// Create a tracer provider with resource
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String("evergreen-agent"),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)

	// Set the global tracer provider
	otel.SetTracerProvider(tp)

	return ctxWithSpan, nil
}
