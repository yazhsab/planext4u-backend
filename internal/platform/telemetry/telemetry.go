package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/yazhsab/planext4u-backend/internal/platform/logging"
)

const instrumentationName = "github.com/yazhsab/planext4u-backend/internal/platform/telemetry"

type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	TraceRatio     float64
}

type Telemetry struct {
	tracer          trace.Tracer
	propagator      propagation.TextMapPropagator
	requestCount    metric.Int64Counter
	requestDuration metric.Float64Histogram
	activeRequests  metric.Int64UpDownCounter
	dependencyCount metric.Int64Counter
	dependencyTime  metric.Float64Histogram
	queueLag        metric.Float64Histogram
	shutdown        func(context.Context) error
}

func Setup(ctx context.Context, config Config) (*Telemetry, error) {
	if !validConfig(config) {
		return nil, errors.New("invalid telemetry configuration")
	}
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(config.ServiceName), semconv.ServiceVersion(config.ServiceVersion), semconv.DeploymentEnvironmentName(config.Environment)))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.TraceRatio))),
		sdktrace.WithBatcher(traceExporter),
	)
	metricProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(30*time.Second))),
	)
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	telemetry, err := New(tracerProvider, metricProvider, propagator)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		_ = metricProvider.Shutdown(ctx)
		return nil, err
	}
	telemetry.shutdown = func(shutdownContext context.Context) error {
		return errors.Join(metricProvider.Shutdown(shutdownContext), tracerProvider.Shutdown(shutdownContext))
	}
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(metricProvider)
	otel.SetTextMapPropagator(propagator)
	return telemetry, nil
}

func New(tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider, propagator propagation.TextMapPropagator) (*Telemetry, error) {
	if tracerProvider == nil || meterProvider == nil || propagator == nil {
		return nil, errors.New("telemetry providers are required")
	}
	meter := meterProvider.Meter(instrumentationName)
	requestCount, err := meter.Int64Counter("planext4u.http.server.requests", metric.WithDescription("Completed inbound HTTP requests"))
	if err != nil {
		return nil, err
	}
	requestDuration, err := meter.Float64Histogram("planext4u.http.server.duration", metric.WithUnit("s"), metric.WithDescription("Inbound HTTP request duration"))
	if err != nil {
		return nil, err
	}
	activeRequests, err := meter.Int64UpDownCounter("planext4u.http.server.active_requests", metric.WithDescription("Currently active inbound HTTP requests"))
	if err != nil {
		return nil, err
	}
	dependencyCount, err := meter.Int64Counter("planext4u.dependency.requests", metric.WithDescription("Completed dependency requests"))
	if err != nil {
		return nil, err
	}
	dependencyTime, err := meter.Float64Histogram("planext4u.dependency.duration", metric.WithUnit("s"), metric.WithDescription("Dependency request duration"))
	if err != nil {
		return nil, err
	}
	queueLag, err := meter.Float64Histogram("planext4u.messaging.queue_lag", metric.WithUnit("s"), metric.WithDescription("Age of a message when processing starts"))
	if err != nil {
		return nil, err
	}
	return &Telemetry{tracer: tracerProvider.Tracer(instrumentationName), propagator: propagator, requestCount: requestCount, requestDuration: requestDuration,
		activeRequests: activeRequests, dependencyCount: dependencyCount, dependencyTime: dependencyTime, queueLag: queueLag, shutdown: func(context.Context) error { return nil }}, nil
}

func (telemetry *Telemetry) Shutdown(ctx context.Context) error { return telemetry.shutdown(ctx) }

func (telemetry *Telemetry) SafeAttributes(values map[string]string) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(values))
	for key, value := range values {
		if logging.IsSensitiveKey(key) {
			value = "[REDACTED]"
		}
		if len(value) > 256 {
			value = value[:256]
		}
		result = append(result, attribute.String(key, value))
	}
	return result
}

func validConfig(config Config) bool {
	return strings.TrimSpace(config.ServiceName) != "" && strings.TrimSpace(config.ServiceVersion) != "" &&
		(config.Environment == "development" || config.Environment == "staging" || config.Environment == "production") && config.TraceRatio >= 0 && config.TraceRatio <= 1
}
