package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestBEOBS001HTTPToMessageTraceMetricsAndRedaction(t *testing.T) {
	t.Parallel()
	telemetry, recorder, reader := testTelemetry(t)
	var emittedTraceparent string
	handler := telemetry.HTTPMiddleware(func(*http.Request) string { return "/v1/customers/{customer_id}" })(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		emittedTraceparent = telemetry.Traceparent(request.Context())
		consumerContext, consumerSpan := telemetry.StartConsumer(context.Background(), "notification-worker", "planext4u.notification.delivery.requested.v1", emittedTraceparent, time.Now().Add(-time.Second))
		traceID, _ := TraceContext(consumerContext)
		if traceID == "" {
			t.Error("consumer trace ID is empty")
		}
		consumerSpan.End()
		writer.WriteHeader(http.StatusAccepted)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/customers/private-customer-42?access_token=raw-secret", nil)
	request.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || emittedTraceparent == "" {
		t.Fatalf("response = %d, traceparent = %q", response.Code, emittedTraceparent)
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext().TraceID().String() != "11111111111111111111111111111111" {
			t.Errorf("span %s trace ID = %s", span.Name(), span.SpanContext().TraceID())
		}
		for _, item := range span.Attributes() {
			encoded := string(item.Key) + "=" + item.Value.Emit()
			if strings.Contains(encoded, "private-customer-42") || strings.Contains(encoded, "raw-secret") || strings.Contains(encoded, "access_token") {
				t.Errorf("span %s leaked sensitive request data in %q", span.Name(), encoded)
			}
		}
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, scope := range metrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			names[item.Name] = true
		}
	}
	for _, name := range []string{"planext4u.http.server.requests", "planext4u.http.server.duration", "planext4u.http.server.active_requests", "planext4u.messaging.queue_lag"} {
		if !names[name] {
			t.Errorf("missing metric %s in %v", name, names)
		}
	}
}

func TestBEOBS001OutboundPropagationAvoidsURLAndHeaderCapture(t *testing.T) {
	t.Parallel()
	telemetry, recorder, _ := testTelemetry(t)
	var propagated string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		propagated = request.Header.Get("traceparent")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, parent := telemetry.tracer.Start(context.Background(), "parent")
	client := &http.Client{Transport: telemetry.Transport("identity", nil)}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/private/path?token=raw-secret", nil)
	request.Header.Set("Authorization", "Bearer raw-secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	parent.End()
	if propagated == "" {
		t.Fatal("outbound traceparent was not propagated")
	}
	for _, span := range recorder.Ended() {
		for _, item := range span.Attributes() {
			encoded := string(item.Key) + "=" + item.Value.Emit()
			if strings.Contains(encoded, "private/path") || strings.Contains(encoded, "raw-secret") || strings.Contains(strings.ToLower(encoded), "authorization") {
				t.Errorf("outbound span leaked request data in %q", encoded)
			}
		}
	}
}

func TestSafeAttributesRedactsSensitiveKeys(t *testing.T) {
	t.Parallel()
	telemetry, _, _ := testTelemetry(t)
	attributes := telemetry.SafeAttributes(map[string]string{"order_id": "order-1", "customer_email": "person@example.com", "api-token": "raw-secret"})
	values := map[string]string{}
	for _, item := range attributes {
		values[string(item.Key)] = item.Value.AsString()
	}
	if values["order_id"] != "order-1" || values["customer_email"] != "[REDACTED]" || values["api-token"] != "[REDACTED]" {
		t.Fatalf("safe attributes = %v", values)
	}
}

func TestTelemetryConfigValidation(t *testing.T) {
	t.Parallel()
	if !validConfig(Config{ServiceName: "catalog", ServiceVersion: "abc123", Environment: "staging", TraceRatio: 0.25}) {
		t.Fatal("valid telemetry configuration was rejected")
	}
	for _, invalid := range []Config{
		{ServiceName: "", ServiceVersion: "v1", Environment: "staging", TraceRatio: 1},
		{ServiceName: "catalog", ServiceVersion: "", Environment: "staging", TraceRatio: 1},
		{ServiceName: "catalog", ServiceVersion: "v1", Environment: "qa", TraceRatio: 1},
		{ServiceName: "catalog", ServiceVersion: "v1", Environment: "production", TraceRatio: 1.1},
	} {
		if validConfig(invalid) {
			t.Fatalf("invalid telemetry configuration accepted: %#v", invalid)
		}
	}
}

func testTelemetry(t *testing.T) (*Telemetry, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	telemetry, err := New(tracerProvider, meterProvider, propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
		_ = tracerProvider.Shutdown(context.Background())
	})
	return telemetry, spanRecorder, reader
}
