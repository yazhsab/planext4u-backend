package fulfillment

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRiderReviewDocumentAccessRequiresTypedPurpose(t *testing.T) {
	service, _ := fulfillmentFixture(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/rider/applications/10000000-0000-4000-8000-000000000001/review/documents/DRIVER_LICENSE/access"
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "missing body", want: http.StatusUnprocessableEntity},
		{name: "missing purpose", body: `{}`, want: http.StatusUnprocessableEntity},
		{name: "wrong purpose", body: `{"purpose":"DOWNLOAD"}`, want: http.StatusUnprocessableEntity},
		{name: "asset ID rejected", body: `{"purpose":"ONBOARDING_REVIEW","asset_id":"forged"}`, want: http.StatusUnprocessableEntity},
		{name: "trailing command", body: `{"purpose":"ONBOARDING_REVIEW"}{}`, want: http.StatusUnprocessableEntity},
		{name: "valid command reaches service", body: `{"purpose":"ONBOARDING_REVIEW"}`, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := fulfillmentRequest(http.MethodPost, path, []byte(test.body), "ops-review-001", "OPS_ADMIN", true)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}
