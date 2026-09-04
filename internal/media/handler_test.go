package media

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMediaHandlerResolvesTypedPublicPresentation(t *testing.T) {
	t.Parallel()
	service, objects, _, _ := mediaFixture(t)
	grant, _ := service.Presign(context.Background(), "tenant-synthetic", "IN", "vendor-a", validRequest())
	objects.metadata[grant.Asset.ObjectKey] = ObjectMetadata{ContentType: grant.Asset.ContentType, SizeBytes: grant.Asset.SizeBytes, SHA256: grant.Asset.SHA256}
	if _, err := service.Complete(context.Background(), "tenant-synthetic", "vendor-a", grant.Asset.ID); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(service)
	request := httptest.NewRequest(http.MethodPost, "/v1/media/presentations:resolve", bytes.NewBufferString(`{"asset_ids":["`+grant.Asset.ID+`"]}`))
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic")
	request.Header.Set("X-Planext4u-Country", "IN")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"asset_id":"`+grant.Asset.ID+`"`) ||
		!strings.Contains(body, `"alt_text":"Fresh milk bottle"`) || !strings.Contains(body, `"width":1200`) ||
		!strings.Contains(body, `"expires_at":`) || strings.Contains(body, `"owner_id"`) {
		t.Fatalf("response=%d %s", response.Code, body)
	}
}
