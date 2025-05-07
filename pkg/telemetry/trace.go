package telemetry

import (
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"
	"os"
)

var TRACER = otel.Tracer("mongodb-kubernetes-operator")

// SetupTracing initializes OpenTelemetry tracing from environment variables
func SetupTracing(ctx context.Context) (context.Context, error) {
	// Get trace and span IDs from environment variables
	traceIDHex := os.Getenv("otel_trace_id")
	spanIDHex := os.Getenv("otel_parent_id")
	endpoint := os.Getenv("otel_collector_endpoint")

	if traceIDHex == "" || spanIDHex == "" || endpoint == "" {
		Logger.Info("tracing environment variables missing, not configuring tracing")
		return ctx, nil
	}

	traceID, err := trace.TraceIDFromHex(traceIDHex)
	if err != nil {
		return ctx, err
	}
	spanID, err := trace.SpanIDFromHex(spanIDHex)
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
		otlptracegrpc.WithInsecure(),
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
