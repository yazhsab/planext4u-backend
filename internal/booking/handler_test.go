package booking

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBEP4001BookingHTTPContractAndStaleRevision(t *testing.T) {
	service, _, _, _ := bookingFixture(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}

	list := authenticatedRequest(http.MethodGet, "/v1/services?postal_code=600001", nil)
	listResult := httptest.NewRecorder()
	handler.ServeHTTP(listResult, list)
	if listResult.Code != http.StatusOK || !bytes.Contains(listResult.Body.Bytes(), []byte("service-home-cleaning")) {
		t.Fatalf("list status=%d body=%s", listResult.Code, listResult.Body.String())
	}

	holdBody := []byte(`{"slot_id":"slot-cleaning-002","postal_code":"600001"}`)
	holdRequest := authenticatedRequest(http.MethodPost, "/v1/service-slot-holds", holdBody)
	holdRequest.Header.Set("Idempotency-Key", "http-hold-command-0001")
	holdResult := httptest.NewRecorder()
	handler.ServeHTTP(holdResult, holdRequest)
	if holdResult.Code != http.StatusCreated {
		t.Fatalf("hold status=%d body=%s", holdResult.Code, holdResult.Body.String())
	}
	var hold SlotHold
	if err := json.Unmarshal(holdResult.Body.Bytes(), &hold); err != nil {
		t.Fatal(err)
	}

	createBody := []byte(`{"hold_id":"` + hold.ID + `","payment_method":"WALLET"}`)
	createRequest := authenticatedRequest(http.MethodPost, "/v1/service-bookings", createBody)
	createRequest.Header.Set("Idempotency-Key", "http-booking-command-01")
	createResult := httptest.NewRecorder()
	handler.ServeHTTP(createResult, createRequest)
	if createResult.Code != http.StatusCreated || createResult.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d etag=%s body=%s", createResult.Code, createResult.Header().Get("ETag"), createResult.Body.String())
	}
	var booking Booking
	if err := json.Unmarshal(createResult.Body.Bytes(), &booking); err != nil {
		t.Fatal(err)
	}

	cancelBody := []byte(`{"reason":"Schedule changed"}`)
	cancelRequest := authenticatedRequest(http.MethodPost, "/v1/service-bookings/"+booking.ID+"/cancel", cancelBody)
	cancelRequest.Header.Set("Idempotency-Key", "http-cancel-command-0001")
	cancelRequest.Header.Set("If-Match", `"99"`)
	cancelResult := httptest.NewRecorder()
	handler.ServeHTTP(cancelResult, cancelRequest)
	if cancelResult.Code != http.StatusConflict {
		t.Fatalf("stale cancellation status=%d body=%s", cancelResult.Code, cancelResult.Body.String())
	}

	cancelRequest = authenticatedRequest(http.MethodPost, "/v1/service-bookings/"+booking.ID+"/cancel", cancelBody)
	cancelRequest.Header.Set("Idempotency-Key", "http-cancel-command-0002")
	cancelRequest.Header.Set("If-Match", `"1"`)
	cancelResult = httptest.NewRecorder()
	handler.ServeHTTP(cancelResult, cancelRequest)
	if cancelResult.Code != http.StatusOK || cancelResult.Header().Get("ETag") != `"3"` || !bytes.Contains(cancelResult.Body.Bytes(), []byte(`"status":"CANCELLED"`)) {
		t.Fatalf("cancel status=%d etag=%s body=%s", cancelResult.Code, cancelResult.Header().Get("ETag"), cancelResult.Body.String())
	}
}

func TestBEP4001BookingHTTPRejectsRoleForgeryAndUnknownFields(t *testing.T) {
	service, _, _, _ := bookingFixture(t)
	handler, _ := NewHandler(service)

	request := authenticatedRequest(http.MethodPost, "/v1/service-slot-holds", []byte(`{"slot_id":"slot-cleaning-001","postal_code":"600001","admin":true}`))
	request.Header.Set("Idempotency-Key", "http-strict-body-command")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status=%d body=%s", result.Code, result.Body.String())
	}

	request = authenticatedRequest(http.MethodGet, "/v1/services?postal_code=600001", nil)
	request.Header.Set("X-Planext4u-Roles", "RIDER")
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden {
		t.Fatalf("cross-role status=%d body=%s", result.Code, result.Body.String())
	}
}

func authenticatedRequest(method, target string, body []byte) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", testScope().TenantID)
	request.Header.Set("X-Planext4u-Country", testScope().Country)
	request.Header.Set("X-Planext4u-Subject", testScope().CustomerID)
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	request.Header.Set("X-Correlation-ID", "corr-synthetic-booking")
	return request
}
