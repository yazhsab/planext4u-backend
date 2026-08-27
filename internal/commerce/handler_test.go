package commerce

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRequiresCustomerScopeAndMutationControls(t *testing.T) {
	t.Parallel()
	service, _ := NewService(syntheticProvider(), fixedClock)
	handler, _ := NewHandler(service)

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1/cart", nil))
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "CART_ACCESS_DENIED") {
		t.Fatalf("denied = %d %s", denied.Code, denied.Body.String())
	}

	request := httptest.NewRequest(http.MethodPut, "/v1/cart/items/variant-oil-1l", strings.NewReader(`{"quantity":2}`))
	setCustomerHeaders(request)
	request.Header.Set("Idempotency-Key", "idem-handler-command-0001")
	request.Header.Set("If-Match", `"0"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` || !strings.Contains(response.Body.String(), `"amount_minor":90000`) {
		t.Fatalf("put = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	replayRequest := httptest.NewRequest(http.MethodPut, "/v1/cart/items/variant-oil-1l", strings.NewReader(`{"quantity":2}`))
	setCustomerHeaders(replayRequest)
	replayRequest.Header.Set("Idempotency-Key", "idem-handler-command-0001")
	replayRequest.Header.Set("If-Match", `"0"`)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK || replayResponse.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("replay = %d %v", replayResponse.Code, replayResponse.Header())
	}
}

func setCustomerHeaders(request *http.Request) {
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-synthetic-001")
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	request.Header.Set("X-Correlation-ID", "corr-commerce-test")
}
