package fakeprovider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyntheticKYCAttestationsRequireExplicitExactDecision(t *testing.T) {
	t.Parallel()
	handler := NewHandler()
	scope := syntheticKYCScope{
		TenantID: "afc1e0db-73cf-40b3-9927-33590133da0b", Country: "IN",
		OwnerID: "60000000-0000-4000-8000-000000000001", Purpose: "ONBOARDING_KYC",
		Documents: []syntheticKYCDocument{
			{Kind: "IDENTITY_PROOF", AssetID: "90000000-0000-4000-8000-000000000001"},
			{Kind: "BUSINESS_LICENSE", AssetID: "90000000-0000-4000-8000-000000000002"},
		},
	}
	call := func(path string, body any, credential string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set("Authorization", credential)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	resolve := "/v1/identity-documents/verification:resolve"
	issue := "/synthetic/identity-documents/attestations"
	credential := "Bearer " + SyntheticKYCToken
	if got := call(resolve, scope, credential).Code; got != http.StatusNotFound {
		t.Fatalf("unissued attestation resolved: %d", got)
	}
	if got := call(issue, scope, "Bearer invalid").Code; got != http.StatusUnauthorized {
		t.Fatalf("untrusted issuance accepted: %d", got)
	}
	issueBody := map[string]any{"tenant_id": scope.TenantID, "country": scope.Country, "owner_id": scope.OwnerID, "purpose": scope.Purpose, "documents": scope.Documents, "status": "REJECTED"}
	if got := call(issue, issueBody, credential).Code; got != http.StatusCreated {
		t.Fatalf("rejected fixture not issued: %d", got)
	}
	if got := call(resolve, scope, credential).Code; got != http.StatusForbidden {
		t.Fatalf("rejected attestation resolved: %d", got)
	}
	issueBody["status"] = "VERIFIED"
	if got := call(issue, issueBody, credential).Code; got != http.StatusCreated {
		t.Fatalf("verified fixture not issued: %d", got)
	}
	response := call(resolve, scope, credential)
	if response.Code != http.StatusOK {
		t.Fatalf("matching attestation not resolved: %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		syntheticKYCScope
		AttestationID string `json:"attestation_id"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Status != "VERIFIED" || result.AttestationID == "" || result.OwnerID != scope.OwnerID || len(result.Documents) != 2 {
		t.Fatalf("wrong scoped attestation: %#v err=%v", result, err)
	}
	wrongOwner := scope
	wrongOwner.OwnerID = "60000000-0000-4000-8000-000000000002"
	if got := call(resolve, wrongOwner, credential).Code; got != http.StatusNotFound {
		t.Fatalf("foreign owner reused attestation: %d", got)
	}
	wrongAsset := scope
	wrongAsset.Documents = append([]syntheticKYCDocument(nil), scope.Documents...)
	wrongAsset.Documents[0].AssetID = "90000000-0000-4000-8000-000000000003"
	if got := call(resolve, wrongAsset, credential).Code; got != http.StatusNotFound {
		t.Fatalf("changed document reused attestation: %d", got)
	}
	if got := call(resolve, scope, "Bearer invalid").Code; got != http.StatusUnauthorized {
		t.Fatalf("untrusted resolver accepted: %d", got)
	}
}
