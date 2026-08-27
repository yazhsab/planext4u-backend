package configcms

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBootstrapHandlerUsesTrustedScopeAndCachingHeaders(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(validSnapshot(now))
	service, _ := NewService(repository, func() time.Time { return now })
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/bootstrap?platform=android&app_version=1.0.0&locale=ta", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"config-1"` ||
		!strings.Contains(response.Header().Get("Cache-Control"), "stale-if-error") ||
		!strings.Contains(response.Body.String(), `"update_gate":"OPTIONAL"`) {
		t.Fatalf("response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
}

func TestBootstrapHandlerReturnsSafeProblem(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(validSnapshot(now))
	service, _ := NewService(repository, func() time.Time { return now })
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/bootstrap?platform=android&app_version=bad", nil)
	request.Header.Set("X-Correlation-ID", "corr-synthetic-config")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body, _ := io.ReadAll(response.Body)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "BOOTSTRAP_REQUEST_INVALID") ||
		!strings.Contains(string(body), "corr-synthetic-config") {
		t.Fatalf("response = %d %s", response.Code, body)
	}
}
