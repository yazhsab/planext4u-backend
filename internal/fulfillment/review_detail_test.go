package fulfillment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type riderReviewStub struct{ Application }

func (riderReviewStub) RiderReview(_ context.Context, _ Actor, id string) (RiderReviewDetail, error) {
	return RiderReviewDetail{RiderID: id, ReviewActions: []string{"APPROVE"}, Documents: []RiderReviewDocument{}}, nil
}

func TestRiderReviewDetailHTTPRoute(t *testing.T) {
	handler, err := NewHandler(riderReviewStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := fulfillmentRequest(http.MethodGet, "/v1/rider/applications/60000000-0000-4000-8000-000000000001/review", nil, "reviewer", "OPS_ADMIN", true)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"review_actions":["APPROVE"]`) {
		t.Fatalf("review route status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestRiderReviewDetailRedactsPrivateEvidence(t *testing.T) {
	value := RiderProfile{ID: "application", RiderID: "rider", Revision: 4, Status: RiderKYCReview,
		FullName: "Fixture rider", VehicleType: "CAR", VehicleNumber: "TN012345", BankReference: "bankref_private_secret",
		Documents: []RiderDocument{{Kind: "IDENTITY", AssetID: "90000000-0000-4000-8000-000000000901", OCRStatus: "PENDING"}, {Kind: "DRIVER_LICENSE", AssetID: "90000000-0000-4000-8000-000000000902"}, {Kind: "VEHICLE_REGISTRATION", AssetID: "90000000-0000-4000-8000-000000000903"}, {Kind: "INSURANCE", AssetID: "90000000-0000-4000-8000-000000000904"}}, Zones: []string{"600001"}}
	result := safeRiderReview(value)
	if result.VehicleNumberMasked != "••••2345" || len(result.ReviewActions) != 2 {
		t.Fatalf("review projection: %+v valid=%t docs=%t uuid=%t bank=%t", result,
			validRiderRegistration(RiderRegistrationRequest{FullName: value.FullName, PhoneMasked: value.PhoneMasked, VehicleType: value.VehicleType, VehicleNumber: value.VehicleNumber, Documents: value.Documents, BankReference: value.BankReference, Zones: value.Zones}),
			validRiderDocumentSet(value.VehicleType, value.Documents), allFulfillmentUUIDDocuments(value.Documents), riderBankReferencePattern.MatchString(value.BankReference))
	}
	value.VehicleNumber = "AB12"
	if short := safeRiderReview(value).VehicleNumberMasked; short != "••••B12" {
		t.Fatalf("short vehicle number was not masked: %q", short)
	}
	value.VehicleNumber = "TN012345"
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"90000000-0000-4000-8000-000000000901", "90000000-0000-4000-8000-000000000902", "bankref_private_secret", "TN012345", "asset_id", "bank_reference"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("review leaked %q: %s", secret, encoded)
		}
	}
	value.Status = RiderApproved
	if actions := safeRiderReview(value).ReviewActions; len(actions) != 0 {
		t.Fatalf("approved application offered review actions: %+v", actions)
	}
}
