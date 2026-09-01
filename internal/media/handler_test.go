package media

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMediaHandlerDeniesMissingAndCrossOwnerScope(t *testing.T) {
	t.Parallel()
	service, _, _, _ := mediaFixture(t)
	handler, _ := NewHandler(service)

	request := httptest.NewRequest(http.MethodPost, "/v1/media/uploads", bytes.NewBufferString(`{"purpose":"CATALOG_IMAGE","content_type":"image/jpeg","size_bytes":4096,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing scope status = %d", response.Code)
	}

	grant, _ := service.Presign(request.Context(), "tenant-synthetic", "IN", "owner-a", validRequest())
	request = httptest.NewRequest(http.MethodGet, "/v1/media/"+grant.Asset.ID, nil)
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "owner-b")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-owner status = %d body=%s", response.Code, response.Body.String())
	}
}
