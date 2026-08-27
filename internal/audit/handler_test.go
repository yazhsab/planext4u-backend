package audit

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuditHandlerDeniesForgedActorAndMissingCapability(t *testing.T) {
	t.Parallel()
	service, _, _ := fixture(t)
	handler, _ := NewHandler(service)
	body := []byte(`{"country":"IN","actor":{"subject_id":"forged-admin","actor_type":"USER"},"action":"admin.role.updated","target":{"type":"customer","id":"customer-synthetic"},"outcome":"SUCCEEDED","reason_code":"APPROVED_CHANGE","correlation_id":"corr-synthetic-audit","occurred_at":"2026-08-27T09:59:00Z"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/audit/events", bytes.NewReader(body))
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Subject", "admin-synthetic")
	request.Header.Set("X-Planext4u-Capabilities", "audit.write")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forged actor status = %d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/admin/audit/events", nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Subject", "admin-synthetic")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read denial status = %d", response.Code)
	}
}
