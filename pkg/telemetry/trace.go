package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
)

var TRACER = otel.Tracer("mongodb-kubernetes-operator")

// SetupTracing initializes OpenTelemetry tracing from environment variables
func SetupTracing(ctx context.Context, traceIDHex, parentIDHex, endpoint string) (context.Context, *sdktrace.TracerProvider, error) {
	if traceIDHex == "" || parentIDHex == "" || endpoint == "" {
		Logger.Debug("tracing environment variables missing, not configuring tracing")
		return ctx, nil, nil
	}

	Logger.Debugf("Setting up tracing with traceIDHex=%s, parentIDHex=%s, endpoint=%s", traceIDHex, parentIDHex, endpoint)

	traceID, err := trace.TraceIDFromHex(traceIDHex)
	if err != nil {
		Logger.Warnf("Failed to parse trace ID: %v", err)
		return ctx, nil, err
	}
	parentSpanID, err := trace.SpanIDFromHex(parentIDHex)
	if err != nil {
		Logger.Warnf("Failed to parse parent span ID: %v", err)
		return ctx, nil, err
	}

	Logger.Debugf("Using trace ID: %s, parent span ID: %s", traceID.String(), parentSpanID.String())
	// Create a span context that marks this as a remote parent span context
	// This allows the operator to create spans that are children of the e2e test spans
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     parentSpanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})

	ctxWithSpan := trace.ContextWithRemoteSpanContext(ctx, sc)

	spanCtx := trace.SpanContextFromContext(ctxWithSpan)
	Logger.Debugf("Created span context with TraceID: %s, SpanID: %s, Remote: %t",
		spanCtx.TraceID().String(), spanCtx.SpanID().String(), spanCtx.IsRemote())

	exporter, err := otlptracegrpc.New(
		context.Background(),
		otlptracegrpc.WithEndpoint(endpoint),
	)
	if err != nil {
		Logger.Warnf("Failed to create OTLP exporter: %v", err)
		return ctx, nil, err
	}

	bsp := sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithBatchTimeout(5*time.Second),
	)

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String("mongodb-kubernetes-operator"),
		attribute.String("component", "operator"),
		// the trace we want the root to attach to
		attribute.String("trace.id", traceID.String()),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Set the global tracer provider
	otel.SetTracerProvider(tp)

	return ctxWithSpan, tp, nil
}
