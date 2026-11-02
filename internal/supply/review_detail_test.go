package supply

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type vendorReviewStub struct{ ApplicationService }

func (vendorReviewStub) VendorReview(_ context.Context, _ Actor, id string) (VendorReviewDetail, error) {
	return VendorReviewDetail{VendorID: id, ReviewActions: []string{"START_OCR_REVIEW"}, Documents: []VendorReviewDocument{}}, nil
}

func TestVendorReviewDetailHTTPRoute(t *testing.T) {
	handler, err := NewHandler(vendorReviewStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := supplyRequest(http.MethodGet, "/v1/vendor/applications/60000000-0000-4000-8000-000000000001/review", nil, "reviewer", "OPS_ADMIN")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"review_actions":["START_OCR_REVIEW"]`) {
		t.Fatalf("review route status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestVendorReviewDetailRedactsPrivateEvidenceAndGatesActions(t *testing.T) {
	now := time.Now().UTC()
	value := Application{
		ID: "application", VendorID: "vendor", Revision: 7, Status: StatusBankReview,
		BusinessName: "Fixture business", BusinessType: "SOLE_PROPRIETOR", ContactName: "Fixture contact",
		Documents: []Document{{Kind: "OWNER_IDENTITY", AssetID: "90000000-0000-4000-8000-000000000901", OCRStatus: "VERIFIED", ExtractedFields: map[string]string{"dob": "private-ocr-secret"}}, {Kind: "BUSINESS_REGISTRATION", AssetID: "90000000-0000-4000-8000-000000000902", OCRStatus: "VERIFIED"}},
		Bank:      &BankAccount{Reference: "bankref-private-secret", Last4: "1234", Status: "VERIFIED"},
		Visit:     &FieldVisit{ScheduledAt: now, CheckedInAt: &now}, Zones: []ServiceZone{{ID: "zone"}}, UpdatedAt: now,
	}
	actor := Actor{Roles: []string{"OPS_ADMIN"}}
	result := safeVendorReview(value, actor)
	if len(result.ReviewActions) != 2 || result.ReviewActions[0] != "REJECT" || result.ReviewActions[1] != "APPROVE" {
		t.Fatalf("ready review actions: %+v", result.ReviewActions)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"90000000-0000-4000-8000-000000000901", "90000000-0000-4000-8000-000000000902", "private-ocr-secret", "bankref-private-secret", "asset_id", "extracted_fields", "bank_reference"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("review leaked %q: %s", secret, encoded)
		}
	}
	value.Bank.Status = "PENDING_VERIFICATION"
	result = safeVendorReview(value, actor)
	if len(result.ReviewActions) != 1 || result.ReviewActions[0] != "REJECT" {
		t.Fatalf("unverified bank offered approval: %+v", result.ReviewActions)
	}
	result = safeVendorReview(value, Actor{Roles: []string{"FINANCE"}})
	if len(result.ReviewActions) != 1 || result.ReviewActions[0] != "VERIFY_BANK" {
		t.Fatalf("finance review actions: %+v", result.ReviewActions)
	}
}
