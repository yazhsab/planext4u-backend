package fulfillment

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBEP4008FulfillmentHTTPStrictPayloadRevisionAndMFA(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := fulfillmentRequest(http.MethodPost, "/v1/rider/applications", []byte(`{"full_name":"HTTP Rider","phone_masked":"******1234","vehicle_type":"MOTORBIKE","vehicle_number":"TN01AB1234","documents":[{"kind":"DRIVER_LICENSE","asset_id":"asset-http-license"},{"kind":"IDENTITY","asset_id":"asset-http-identity"}],"bank_reference":"bankref_http0001","zones":["600001"],"is_admin":true}`), "rider-http-001", "RIDER", false)
	request.Header.Set("Idempotency-Key", "http-rider-register-01")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status=%d body=%s", result.Code, result.Body.String())
	}
	request = fulfillmentRequest(http.MethodGet, "/v1/operations/regions/region-chennai/dashboard", nil, "ops-http-001", "OPS_ADMIN", false)
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden || !bytes.Contains(result.Body.Bytes(), []byte("FULFILLMENT_MFA_REQUIRED")) {
		t.Fatalf("MFA status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestBEP4009FulfillmentHTTPChatRejectsRoleForgery(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	admin := fulfillmentActor("ops-admin-001", "OPS_ADMIN", true)
	seed := fulfillmentTaskSeed("delivery-http-chat-01", "food-order-http-chat")
	if _, err := service.SeedTask(admin, seed); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(service)
	request := fulfillmentRequest(http.MethodGet, "/v1/order-chats/"+seed.OrderID, nil, "attacker-http-chat", "RIDER", false)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("chat actor scope status=%d body=%s", result.Code, result.Body.String())
	}
}

func fulfillmentRequest(method, target string, body []byte, subject, role string, mfa bool) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", subject)
	request.Header.Set("X-Planext4u-Roles", role)
	request.Header.Set("X-Correlation-ID", "corr-synthetic-fulfillment")
	if mfa {
		request.Header.Set("X-Planext4u-MFA", "verified")
	}
	return request
}
