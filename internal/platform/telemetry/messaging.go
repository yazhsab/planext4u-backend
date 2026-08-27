package telemetry

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func (telemetry *Telemetry) Traceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	telemetry.propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

func (telemetry *Telemetry) StartConsumer(ctx context.Context, consumer, eventType, traceparent string, occurredAt time.Time) (context.Context, trace.Span) {
	carrier := propagation.MapCarrier{"traceparent": strings.TrimSpace(traceparent)}
	ctx = telemetry.propagator.Extract(ctx, carrier)
	consumer = safeDependency(consumer)
	eventType = safeEventType(eventType)
	ctx, span := telemetry.tracer.Start(ctx, consumer+" process", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("messaging.consumer.name", consumer), attribute.String("messaging.message.type", eventType)))
	lag := time.Since(occurredAt).Seconds()
	if lag < 0 {
		lag = 0
	}
	telemetry.queueLag.Record(ctx, lag, metric.WithAttributes(attribute.String("messaging.consumer.name", consumer), attribute.String("messaging.message.type", eventType)))
	return ctx, span
}

func safeEventType(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 160 || !strings.HasPrefix(value, "planext4u.") {
		return "unknown"
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_') {
			return "unknown"
		}
	}
	return value
}
