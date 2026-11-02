package fakeprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/ocrattestation"
)

func TestSyntheticOCRRequiresExplicitExactDocumentDecision(t *testing.T) {
	t.Parallel()
	handler := NewHandler()
	server := httptest.NewServer(handler)
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	verifier, err := ocrattestation.NewHTTPVerifier(endpoint,
		&http.Client{Timeout: time.Second}, SyntheticOCRToken, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	scope := ocrattestation.Scope{
		TenantID: "10000000-0000-4000-8000-000000000001", Country: "IN",
		OwnerID: "20000000-0000-4000-8000-000000000001", DocumentRevision: 3,
	}
	documents := []ocrattestation.Document{
		{Kind: "OWNER_IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000001"},
		{Kind: "BUSINESS_REGISTRATION", AssetID: "a0000000-0000-4000-8000-000000000002"},
	}
	if _, err := verifier.Verify(context.Background(), scope, documents); !errors.Is(err, ocrattestation.ErrRejected) {
		t.Fatalf("unissued OCR decision = %v", err)
	}
	call := func(path string, value any, credential string) *httptest.ResponseRecorder {
		t.Helper()
		body, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Authorization", credential)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	issue := "/synthetic/documents/ocr-attestations"
	resolve := "/v1/documents/ocr-verification:resolve"
	credential := "Bearer " + SyntheticOCRToken
	decision := map[string]any{
		"tenant_id": scope.TenantID, "country": scope.Country, "owner_id": scope.OwnerID,
		"document_revision": scope.DocumentRevision, "purpose": "VENDOR_ONBOARDING_OCR",
		"documents": documents, "status": "REJECTED",
	}
	if code := call(issue, decision, "Bearer invalid").Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthorized OCR issuance = %d", code)
	}
	if code := call(issue, decision, credential).Code; code != http.StatusCreated {
		t.Fatalf("rejected OCR issuance = %d", code)
	}
	if _, err := verifier.Verify(context.Background(), scope, documents); !errors.Is(err, ocrattestation.ErrRejected) {
		t.Fatalf("rejected OCR decision = %v", err)
	}
	decision["status"] = "VERIFIED"
	if code := call(issue, decision, credential).Code; code != http.StatusCreated {
		t.Fatalf("verified OCR issuance = %d", code)
	}
	id, err := verifier.Verify(context.Background(), scope, documents)
	if err != nil || id == "" {
		t.Fatalf("exact OCR decision = %q, %v", id, err)
	}
	reordered := []ocrattestation.Document{documents[1], documents[0]}
	if replayID, err := verifier.Verify(context.Background(), scope, reordered); err != nil || replayID != id {
		t.Fatalf("canonical document order = %q, %v", replayID, err)
	}
	for _, changed := range []struct {
		name      string
		scope     ocrattestation.Scope
		documents []ocrattestation.Document
	}{
		{"foreign tenant", ocrattestation.Scope{TenantID: "10000000-0000-4000-8000-000000000002", Country: "IN", OwnerID: scope.OwnerID, DocumentRevision: 3}, documents},
		{"foreign owner", ocrattestation.Scope{TenantID: scope.TenantID, Country: "IN", OwnerID: "20000000-0000-4000-8000-000000000002", DocumentRevision: 3}, documents},
		{"different country", ocrattestation.Scope{TenantID: scope.TenantID, Country: "GB", OwnerID: scope.OwnerID, DocumentRevision: 3}, documents},
		{"stale revision", ocrattestation.Scope{TenantID: scope.TenantID, Country: "IN", OwnerID: scope.OwnerID, DocumentRevision: 4}, documents},
		{"changed asset", scope, []ocrattestation.Document{documents[0], {Kind: documents[1].Kind, AssetID: "a0000000-0000-4000-8000-000000000003"}}},
	} {
		t.Run(changed.name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), changed.scope, changed.documents); !errors.Is(err, ocrattestation.ErrRejected) {
				t.Fatalf("mismatched OCR decision = %v", err)
			}
		})
	}
	wrongPurpose := map[string]any{
		"tenant_id": scope.TenantID, "country": scope.Country, "owner_id": scope.OwnerID,
		"document_revision": scope.DocumentRevision, "purpose": "ONBOARDING_KYC", "documents": documents,
	}
	if code := call(resolve, wrongPurpose, credential).Code; code != http.StatusBadRequest {
		t.Fatalf("foreign purpose resolved OCR decision: %d", code)
	}
	if code := call(resolve, decision, "Bearer invalid").Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthorized OCR resolution = %d", code)
	}
	duplicate := map[string]any{
		"tenant_id": scope.TenantID, "country": scope.Country, "owner_id": scope.OwnerID,
		"document_revision": scope.DocumentRevision, "purpose": "VENDOR_ONBOARDING_OCR",
		"documents": []ocrattestation.Document{documents[0], documents[0]}, "status": "VERIFIED",
	}
	if code := call(issue, duplicate, credential).Code; code != http.StatusBadRequest {
		t.Fatalf("duplicate document received OCR decision: %d", code)
	}
}
