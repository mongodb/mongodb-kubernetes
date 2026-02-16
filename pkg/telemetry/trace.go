package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
)

var TRACER = otel.Tracer("mongodb-kubernetes-operator")

// StartReconcileSpan creates a span for a reconciliation operation.
// Returns the updated context and the span. Caller must call span.End() when done.
func StartReconcileSpan(ctx context.Context, resourceType, namespace, name string) (context.Context, trace.Span) {
	return TRACER.Start(ctx, fmt.Sprintf("reconcile/%s", resourceType),
		trace.WithAttributes(
			attribute.String("k8s.namespace", namespace),
			attribute.String("k8s.name", name),
			attribute.String("resource.type", resourceType),
		))
}

// StartReconcileSpanWithAnnotations creates a span for a reconciliation operation,
// first extracting any trace context from the resource's annotations.
// This enables true parent-child trace relationships when e2e tests inject traceparent annotations.
func StartReconcileSpanWithAnnotations(ctx context.Context, resourceType, namespace, name string, annotations map[string]string) (context.Context, trace.Span) {
	// Extract trace context from annotations if present (Phase 2 feature)
	ctx = ExtractTraceContextFromAnnotations(ctx, annotations)

	return TRACER.Start(ctx, fmt.Sprintf("reconcile/%s", resourceType),
		trace.WithAttributes(
			attribute.String("k8s.namespace", namespace),
			attribute.String("k8s.name", name),
			attribute.String("resource.type", resourceType),
		))
}

// EndReconcileSpan ends a reconcile span, recording any error that occurred.
func EndReconcileSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// TraceparentAnnotation is the W3C standard annotation key for trace context propagation.
const TraceparentAnnotation = "traceparent"

// ExtractTraceContextFromAnnotations extracts W3C traceparent from Kubernetes resource annotations.
// If a valid traceparent annotation is found, it returns a context with the extracted trace context.
// Otherwise, it returns the original context unchanged.
func ExtractTraceContextFromAnnotations(ctx context.Context, annotations map[string]string) context.Context {
	if annotations == nil {
		return ctx
	}
	traceparent, ok := annotations[TraceparentAnnotation]
	if !ok || traceparent == "" {
		return ctx
	}

	// Use W3C TraceContext propagator to extract the trace context
	carrier := propagation.MapCarrier{TraceparentAnnotation: traceparent}
	prop := propagation.TraceContext{}
	return prop.Extract(ctx, carrier)
}

// SetupTracingFromParent initializes OpenTelemetry tracing given a remote trace and span.
func SetupTracingFromParent(ctx context.Context, traceIDHex, parentIDHex, endpoint string) (context.Context, *sdktrace.TracerProvider, error) {
	if traceIDHex == "" || parentIDHex == "" || endpoint == "" {
		Logger.Debug("tracing environment variables missing, not configuring tracing from a remote span and trace")
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

	ctxWithParentSpan := trace.ContextWithRemoteSpanContext(ctx, sc)

	spanCtx := trace.SpanContextFromContext(ctxWithParentSpan)
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

	return ctxWithParentSpan, tp, nil
}
