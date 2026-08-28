package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogHandlerRoutesProjectionAndLocation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	repository, _ := NewMemoryRepository(syntheticCategories(), syntheticItems())
	service, _ := NewService(repository, []Zone{{ID: "zone-chennai", Country: "IN", Locality: "Chennai", MinimumLatitude: 12.8, MaximumLatitude: 13.3, MinimumLongitude: 80, MaximumLongitude: 80.4, PostalCodes: []string{"600001"}}}, 15*time.Minute, func() time.Time { return now })
	handler, _ := NewHandler(service)

	request := httptest.NewRequest(http.MethodGet, "/v1/catalog/search?q=filter&category_id=home-services&limit=1", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Projection-Status") != "FRESH" || !strings.Contains(response.Body.String(), "item-water-filter") {
		t.Fatalf("search response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/catalog/suggestions?q=filter", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":"PRODUCT"`) {
		t.Fatalf("suggestion response = %d %s", response.Code, response.Body.String())
	}

	location := `{"latitude":13.08,"longitude":80.27,"accuracy_metres":10,"captured_at":"2026-08-27T10:00:00Z","purpose":"LOCATION_SERVICEABILITY"}`
	request = httptest.NewRequest(http.MethodPost, "/v1/serviceability/check", strings.NewReader(location))
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"serviceable":true`) {
		t.Fatalf("location response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/geocoding/search?q=600", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"postal_code":"600001"`) {
		t.Fatalf("geocoding response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/catalog/items/item-milk/questions", strings.NewReader(`{"question":"Will this arrive chilled?"}`))
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-001")
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	request.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000001")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "customer-001") || !strings.Contains(response.Body.String(), "Will this arrive chilled?") {
		t.Fatalf("question response = %d %s", response.Code, response.Body.String())
	}
}
