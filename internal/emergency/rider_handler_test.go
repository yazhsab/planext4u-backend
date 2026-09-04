package emergency

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRiderEmergencyRequiresDutyAndBoundsLocationConsent(t *testing.T) {
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	service, err := NewService(Configuration{
		TenantID:       "tenant-synthetic-001",
		Country:        "IN",
		AssignmentSLA:  time.Minute,
		LocationMaxAge: 2 * time.Minute,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	onDuty := false
	handler, err := NewRiderHandler(service, RiderDutyVerifierFunc(func(Actor) (bool, error) {
		return onDuty, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	offDuty := riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents", `{"category":"SAFETY","description":"Rider needs urgent safety assistance","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.03,"longitude":80.27,"accuracy_m":8}}`)
	offDuty.Header.Set("Idempotency-Key", "rider-emergency-create-001")
	offDutyResponse := httptest.NewRecorder()
	handler.ServeHTTP(offDutyResponse, offDuty)
	if offDutyResponse.Code != http.StatusForbidden || !containsJSONCode(offDutyResponse.Body.Bytes(), "RIDER_EMERGENCY_OFF_DUTY") {
		t.Fatalf("off-duty status=%d body=%s", offDutyResponse.Code, offDutyResponse.Body.String())
	}

	onDuty = true
	activeRequest := riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents", `{"category":"SAFETY","description":"Rider needs urgent safety assistance","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.03,"longitude":80.27,"accuracy_m":8}}`)
	activeRequest.Header.Set("Idempotency-Key", "rider-emergency-create-001")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, activeRequest)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created Request
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "OPEN" || created.CurrentLocation == nil || created.AllowedActions[0] != "UPDATE_LOCATION" {
		t.Fatalf("created=%#v", created)
	}
	replayRequest := riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents", `{"category":"SAFETY","description":"Rider needs urgent safety assistance","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.03,"longitude":80.27,"accuracy_m":8}}`)
	replayRequest.Header.Set("Idempotency-Key", "rider-emergency-create-001")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusCreated || replayResponse.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status=%d headers=%v body=%s", replayResponse.Code, replayResponse.Header(), replayResponse.Body.String())
	}

	now = now.Add(3 * time.Minute)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, riderEmergencyRequest(http.MethodGet, "/v1/rider/emergency-incidents/"+created.ID, ""))
	var stale Request
	if getResponse.Code != http.StatusOK || json.Unmarshal(getResponse.Body.Bytes(), &stale) != nil || stale.CurrentLocation != nil {
		t.Fatalf("stale status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}

	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents/"+created.ID+"/location", `{"consent":false,"location":{"latitude":0,"longitude":0,"accuracy_m":0}}`))
	var revoked Request
	if revokeResponse.Code != http.StatusOK || json.Unmarshal(revokeResponse.Body.Bytes(), &revoked) != nil || revoked.LocationConsent || revoked.CurrentLocation != nil {
		t.Fatalf("revoke status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
}

func TestRiderEmergencyRejectsNonRiderAndUnavailableDuty(t *testing.T) {
	service, _ := NewService(Configuration{TenantID: "tenant-synthetic-001", Country: "IN"}, time.Now)
	handler, _ := NewRiderHandler(service, RiderDutyVerifierFunc(func(Actor) (bool, error) {
		return false, ErrDutyUnavailable
	}))

	nonRider := riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents", `{}`)
	nonRider.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	nonRiderResponse := httptest.NewRecorder()
	handler.ServeHTTP(nonRiderResponse, nonRider)
	if nonRiderResponse.Code != http.StatusForbidden {
		t.Fatalf("non-rider status=%d", nonRiderResponse.Code)
	}

	unavailable := riderEmergencyRequest(http.MethodPost, "/v1/rider/emergency-incidents", `{}`)
	unavailableResponse := httptest.NewRecorder()
	handler.ServeHTTP(unavailableResponse, unavailable)
	if unavailableResponse.Code != http.StatusServiceUnavailable || !containsJSONCode(unavailableResponse.Body.Bytes(), "RIDER_DUTY_UNAVAILABLE") {
		t.Fatalf("unavailable status=%d body=%s", unavailableResponse.Code, unavailableResponse.Body.String())
	}
}

func riderEmergencyRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "rider-synthetic-001")
	request.Header.Set("X-Planext4u-Roles", "RIDER")
	return request
}

func containsJSONCode(body []byte, expected string) bool {
	var value struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &value) == nil && value.Error.Code == expected
}
