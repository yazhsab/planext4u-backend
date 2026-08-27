package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/platform/config"
)

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()

	service := newTestServer(t, ":8080")

	health := performRequest(service, "/healthz")
	if health.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", health.Code, http.StatusOK)
	}
	if !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Errorf("GET /healthz body = %q, want ok status", health.Body.String())
	}
	if health.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", health.Header().Get("Cache-Control"))
	}
	if health.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", health.Header().Get("X-Content-Type-Options"))
	}
	if health.Header().Get(requestIDHeader) == "" {
		t.Error("X-Request-ID is empty")
	}

	notReady := performRequest(service, "/readyz")
	if notReady.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz status = %d, want %d", notReady.Code, http.StatusServiceUnavailable)
	}

	service.SetReady(true)
	ready := performRequest(service, "/readyz")
	if ready.Code != http.StatusOK {
		t.Errorf("GET /readyz status = %d, want %d", ready.Code, http.StatusOK)
	}
}

func TestMiddlewarePreservesValidRequestID(t *testing.T) {
	t.Parallel()

	service := newTestServer(t, ":8080")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(requestIDHeader, "request-123")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)

	if got := response.Header().Get(requestIDHeader); got != "request-123" {
		t.Errorf("X-Request-ID = %q, want request-123", got)
	}
}

func TestServerMountsApplicationBehindHardenedMiddleware(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/example" {
			t.Errorf("application path = %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusCreated)
	})
	service := New(config.Config{
		ServiceName: "test-service", Environment: config.EnvironmentDevelopment, HTTPAddress: ":8080",
		ShutdownTimeout: time.Second, LogLevel: "info",
	}, logger, "test", WithApplication(application, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/example", nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get(requestIDHeader) == "" {
		t.Fatalf("application response = %d headers=%v", response.Code, response.Header())
	}
}

func TestServeGracefullyShutsDown(t *testing.T) {
	service := newTestServer(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Serve(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for !service.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !service.Ready() {
		t.Fatal("server did not become ready")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after cancellation")
	}
	if service.Ready() {
		t.Error("server remained ready after shutdown")
	}
}

func performRequest(service *Server, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	return response
}

func newTestServer(t *testing.T, address string) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{
		ServiceName:     "test-service",
		Environment:     config.EnvironmentDevelopment,
		HTTPAddress:     address,
		ShutdownTimeout: time.Second,
		LogLevel:        "info",
	}, logger, "test")
}
