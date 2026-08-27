package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type RouteResolver func(*http.Request) string

func (telemetry *Telemetry) HTTPMiddleware(resolve RouteResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			route := safeRoute(resolve, request)
			ctx := telemetry.propagator.Extract(request.Context(), propagation.HeaderCarrier(request.Header))
			ctx, span := telemetry.tracer.Start(ctx, request.Method+" "+route, trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String("http.request.method", request.Method), attribute.String("http.route", route)))
			defer span.End()
			attributes := []attribute.KeyValue{attribute.String("http.request.method", request.Method), attribute.String("http.route", route)}
			telemetry.activeRequests.Add(ctx, 1, metric.WithAttributes(attributes...))
			started := time.Now()
			recorder := &responseRecorder{ResponseWriter: writer, status: http.StatusOK}
			next.ServeHTTP(recorder, request.WithContext(ctx))
			duration := time.Since(started).Seconds()
			attributes = append(attributes, attribute.Int("http.response.status_code", recorder.status))
			telemetry.activeRequests.Add(ctx, -1, metric.WithAttributes(attributes[:2]...))
			telemetry.requestCount.Add(ctx, 1, metric.WithAttributes(attributes...))
			telemetry.requestDuration.Record(ctx, duration, metric.WithAttributes(attributes...))
			span.SetAttributes(attribute.Int("http.response.status_code", recorder.status))
			if recorder.status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, "server error")
			}
		})
	}
}

func (telemetry *Telemetry) Transport(dependency string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		name := safeDependency(dependency)
		ctx, span := telemetry.tracer.Start(request.Context(), request.Method+" "+name, trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("dependency.name", name), attribute.String("http.request.method", request.Method)))
		defer span.End()
		clone := request.Clone(ctx)
		telemetry.propagator.Inject(ctx, propagation.HeaderCarrier(clone.Header))
		started := time.Now()
		response, err := base.RoundTrip(clone)
		status := "error"
		if response != nil {
			status = strconv.Itoa(response.StatusCode)
			span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		}
		if err != nil {
			span.SetStatus(codes.Error, "dependency request failed")
		}
		attributes := []attribute.KeyValue{attribute.String("dependency.name", name), attribute.String("outcome", status)}
		telemetry.dependencyCount.Add(ctx, 1, metric.WithAttributes(attributes...))
		telemetry.dependencyTime.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attributes...))
		return response, err
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (recorder *responseRecorder) WriteHeader(status int) {
	if recorder.wroteHeader {
		return
	}
	recorder.wroteHeader = true
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *responseRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

func safeRoute(resolve RouteResolver, request *http.Request) string {
	if resolve == nil {
		return "unmatched"
	}
	route := strings.TrimSpace(resolve(request))
	if route == "" || len(route) > 160 || !strings.HasPrefix(route, "/") || strings.ContainsAny(route, "?#\r\n") {
		return "unmatched"
	}
	return route
}

func safeDependency(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return "unknown"
		}
	}
	return value
}

func TraceContext(ctx context.Context) (string, string) {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return "", ""
	}
	return spanContext.TraceID().String(), spanContext.SpanID().String()
}
