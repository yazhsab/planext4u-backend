package supply

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBEP4005SupplyHTTPContractStrictBodyRevisionAndReplay(t *testing.T) {
	service, _ := supplyFixture(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := supplyRequest(http.MethodPost, "/v1/vendor/applications", []byte(`{"business_name":"Planext Vendor","business_type":"Home services","contact_name":"Vendor Owner"}`), "vendor-http-001", "VENDOR")
	request.Header.Set("Idempotency-Key", "http-vendor-register-01")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusCreated || result.Header().Get("ETag") != `"1"` {
		t.Fatalf("register status=%d headers=%v body=%s", result.Code, result.Header(), result.Body.String())
	}
	var value Application
	if err := json.Unmarshal(result.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	replay := supplyRequest(http.MethodPost, "/v1/vendor/applications", []byte(`{"business_name":"Planext Vendor","business_type":"Home services","contact_name":"Vendor Owner"}`), "vendor-http-001", "VENDOR")
	replay.Header.Set("Idempotency-Key", "http-vendor-register-01")
	replayResult := httptest.NewRecorder()
	handler.ServeHTTP(replayResult, replay)
	if replayResult.Code != http.StatusCreated || replayResult.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status=%d headers=%v", replayResult.Code, replayResult.Header())
	}
	documents := supplyRequest(http.MethodPost, "/v1/vendor/application/documents", []byte(`{"documents":[{"kind":"BUSINESS_REGISTRATION","asset_id":"asset-http-business"},{"kind":"OWNER_IDENTITY","asset_id":"asset-http-owner"}],"admin":true}`), "vendor-http-001", "VENDOR")
	documents.Header.Set("Idempotency-Key", "http-vendor-documents1")
	documents.Header.Set("If-Match", `"1"`)
	documentsResult := httptest.NewRecorder()
	handler.ServeHTTP(documentsResult, documents)
	if documentsResult.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status=%d body=%s", documentsResult.Code, documentsResult.Body.String())
	}
	documents = supplyRequest(http.MethodPost, "/v1/vendor/application/documents", []byte(`{"documents":[{"kind":"BUSINESS_REGISTRATION","asset_id":"asset-http-business"},{"kind":"OWNER_IDENTITY","asset_id":"asset-http-owner"}]}`), "vendor-http-001", "VENDOR")
	documents.Header.Set("Idempotency-Key", "http-vendor-documents2")
	documents.Header.Set("If-Match", `"99"`)
	documentsResult = httptest.NewRecorder()
	handler.ServeHTTP(documentsResult, documents)
	if documentsResult.Code != http.StatusConflict || !bytes.Contains(documentsResult.Body.Bytes(), []byte("SUPPLY_REVISION_CONFLICT")) {
		t.Fatalf("stale status=%d body=%s", documentsResult.Code, documentsResult.Body.String())
	}
}

func TestBEP4006SupplyHTTPRejectsCrossRole(t *testing.T) {
	service, _ := supplyFixture(t)
	handler, _ := NewHandler(service)
	request := supplyRequest(http.MethodGet, "/v1/vendor/catalog", nil, "customer-http-001", "CUSTOMER")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("cross-role status=%d body=%s", result.Code, result.Body.String())
	}
}

func supplyRequest(method, target string, body []byte, subject, role string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", subject)
	request.Header.Set("X-Planext4u-Roles", role)
	request.Header.Set("X-Correlation-ID", "corr-synthetic-supply")
	return request
}
