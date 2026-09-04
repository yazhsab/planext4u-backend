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
	request := httptest.NewRequest(http.MethodGet, "/v1/bootstrap?platform=android&app_version=1.0.0&locale=hi", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"config-1"` ||
		!strings.Contains(response.Header().Get("Cache-Control"), "stale-if-error") ||
		!strings.Contains(response.Body.String(), `"update_gate":"OPTIONAL"`) ||
		!strings.Contains(response.Body.String(), `"locale":"hi"`) {
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

func TestBootstrapHandlerSupportsWebDeploymentRefreshGate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(validSnapshot(now))
	service, _ := NewServiceWithWebDeployment(repository, func() time.Time { return now }, "web-current")
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/bootstrap?platform=WEB&app_version=1.2.0&deployment_id=web-stale&locale=en", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"platform":"WEB"`) ||
		!strings.Contains(body, `"client_deployment_id":"web-stale"`) || !strings.Contains(body, `"latest_deployment_id":"web-current"`) ||
		!strings.Contains(body, `"update_gate":"REQUIRED"`) || !strings.Contains(body, `"update_action":"RELOAD"`) {
		t.Fatalf("response = %d %v %s", response.Code, response.Header(), body)
	}
}

func TestPageHandlerReturnsPublishedPageWithRevisionETag(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(now)
	snapshot.Pages = []Page{{ID: "customer-home", Route: "/home", TitleKey: "page.home", Audience: []string{"CUSTOMER"}, Enabled: true, Blocks: []PageBlock{{ID: "hero", Kind: "HERO", Enabled: true, Priority: 10, Content: map[string]any{"title": "Planext4u"}}}}}
	repository, _ := NewMemoryRepository(snapshot)
	service, _ := NewService(repository, func() time.Time { return now })
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/v1/pages/customer-home?locale=ta", nil)
	request.Header.Set("X-Planext4u-Tenant", snapshot.TenantID)
	request.Header.Set("X-Planext4u-Country", snapshot.Country)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"config-1-page-customer-home"` || !strings.Contains(response.Body.String(), `"locale":"ta"`) || !strings.Contains(response.Body.String(), `"title":"Planext4u"`) {
		t.Fatalf("response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
}
