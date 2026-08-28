package food

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBEP4007FoodHTTPContractStrictPayloadAndServerPricing(t *testing.T) {
	service, _ := foodFixture(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := foodRequest(http.MethodGet, "/v1/restaurants?postal_code=600001", nil, "customer-http-food", "CUSTOMER")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !bytes.Contains(result.Body.Bytes(), []byte("Saravana Kitchen")) {
		t.Fatalf("restaurants status=%d body=%s", result.Code, result.Body.String())
	}
	request = foodRequest(http.MethodPost, "/v1/food-carts", []byte(`{"restaurant_id":"restaurant-saravana","postal_code":"600001","lines":[{"menu_item_id":"menu-meals-001","quantity":1,"option_ids":["option-meals-large"]}],"client_total":1}`), "customer-http-food", "CUSTOMER")
	request.Header.Set("Idempotency-Key", "http-food-cart-command1")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusUnprocessableEntity {
		t.Fatalf("client price field status=%d body=%s", result.Code, result.Body.String())
	}
	request = foodRequest(http.MethodGet, "/v1/restaurants?postal_code=600001", nil, "rider-http-food", "RIDER")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("cross-role status=%d body=%s", result.Code, result.Body.String())
	}
}

func foodRequest(method, target string, body []byte, subject, role string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", subject)
	request.Header.Set("X-Planext4u-Roles", role)
	request.Header.Set("X-Correlation-ID", "corr-synthetic-food")
	return request
}
