package verticalslice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	verticalSliceCorrelation = "corr-be-vslice-001"
	verticalSliceTraceID     = "11111111111111111111111111111111"
)

func TestBEVSlice001LoginLocationHomeAndCatalog(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 30, 0, 0, time.UTC)
	application, err := New(Config{
		SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"),
		Clock:      func() time.Time { return now },
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	meterProvider := sdkmetric.NewMeterProvider()
	observability, err := telemetry.New(tracerProvider, meterProvider, propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
		_ = tracerProvider.Shutdown(context.Background())
	})
	server := httptest.NewServer(observability.HTTPMiddleware(Route)(application))
	t.Cleanup(server.Close)

	client := server.Client()
	readiness := verticalRequest(t, client, http.MethodGet, server.URL+"/health/ready", "", "")
	assertVerticalResponse(t, readiness, http.StatusOK, `"status":"ok"`)

	authentication := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/auth/exchange",
		`{"provider":"local","provider_token":"synthetic-customer","device_id":"device-staging-e2e-001","country":"IN"}`, "")
	assertVerticalResponse(t, authentication, http.StatusCreated, `"identity_id":"customer-synthetic-001"`)
	var authPayload struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	decodeVerticalJSON(t, authentication, &authPayload)
	if !strings.HasPrefix(authPayload.Tokens.AccessToken, "p4us_v1.") {
		t.Fatal("synthetic access token was not issued")
	}

	location := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/serviceability/check",
		`{"latitude":13.08,"longitude":80.27,"accuracy_metres":12,"captured_at":"2026-08-27T10:30:00Z","purpose":"LOCATION_SERVICEABILITY"}`,
		authPayload.Tokens.AccessToken)
	assertVerticalResponse(t, location, http.StatusOK, `"serviceable":true`, `"locality":"Chennai"`)

	bootstrap := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/bootstrap?platform=android&app_version=0.1.0&locale=en", "", authPayload.Tokens.AccessToken)
	assertVerticalResponse(t, bootstrap, http.StatusOK, `"customer_home":true`, `"catalog_read":true`)

	home := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/home", "", authPayload.Tokens.AccessToken)
	assertVerticalResponse(t, home, http.StatusOK, `"name":"Daily needs"`, `"name":"Fresh milk"`)

	catalog := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/catalog/items?category_id=daily-needs", "", authPayload.Tokens.AccessToken)
	assertVerticalResponse(t, catalog, http.StatusOK, `"currency":"INR"`, `"name":"Weekly groceries"`)

	spans := spanRecorder.Ended()
	if len(spans) != 6 {
		t.Fatalf("ended spans = %d, want 6", len(spans))
	}
	for _, span := range spans {
		if span.SpanContext().TraceID().String() != verticalSliceTraceID {
			t.Errorf("span %q trace ID = %s", span.Name(), span.SpanContext().TraceID())
		}
	}
}

func TestVerticalSliceRejectsUntrustedOrExpiredIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 30, 0, 0, time.UTC)
	clock := now
	application, err := New(Config{
		SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"),
		Clock:      func() time.Time { return clock },
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)

	unauthorized := verticalRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/home", "", "")
	assertVerticalResponse(t, unauthorized, http.StatusUnauthorized, `"code":"AUTHENTICATION_REQUIRED"`)

	authentication := verticalRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/auth/exchange",
		`{"provider":"local","provider_token":"synthetic-customer","device_id":"device-staging-e2e-002","country":"IN"}`, "")
	var authPayload struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	decodeVerticalJSON(t, authentication, &authPayload)

	tampered := authPayload.Tokens.AccessToken[:len(authPayload.Tokens.AccessToken)-1] + "x"
	invalid := verticalRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/home", "", tampered)
	assertVerticalResponse(t, invalid, http.StatusUnauthorized, `"code":"AUTHENTICATION_INVALID"`)

	clock = now.Add(accessTokenTTL)
	expired := verticalRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/home", "", authPayload.Tokens.AccessToken)
	assertVerticalResponse(t, expired, http.StatusUnauthorized, `"code":"AUTHENTICATION_EXPIRED"`)
}

func verticalRequest(t *testing.T, client *http.Client, method, target, body, accessToken string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-ID", verticalSliceCorrelation)
	request.Header.Set("traceparent", "00-"+verticalSliceTraceID+"-2222222222222222-01")
	request.Header.Set("X-Planext4u-Tenant", "attacker-tenant")
	request.Header.Set("X-Planext4u-Country", "US")
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertVerticalResponse(t *testing.T, response *http.Response, wantStatus int, fragments ...string) {
	t.Helper()
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body = io.NopCloser(bytes.NewReader(contents))
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", response.StatusCode, wantStatus, contents)
	}
	if response.Header.Get("X-Correlation-ID") != verticalSliceCorrelation {
		t.Fatalf("correlation header = %q", response.Header.Get("X-Correlation-ID"))
	}
	for _, fragment := range fragments {
		if !bytes.Contains(contents, []byte(fragment)) {
			t.Errorf("response body %s does not contain %s", contents, fragment)
		}
	}
}

func decodeVerticalJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
