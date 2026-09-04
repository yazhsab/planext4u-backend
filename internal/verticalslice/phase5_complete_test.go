package verticalslice

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBEP5012CompletePhase5GatewayJourney(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	application, err := New(Config{SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"), Clock: func() time.Time { return now }, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	client := server.Client()
	customer := verticalLoginProvider(t, client, server.URL, "synthetic-customer", "device-phase5-customer-001")
	publicCustomer := verticalLoginProvider(t, client, server.URL, "synthetic-public-customer", "device-phase5-public-001")
	moderator := verticalLoginProvider(t, client, server.URL, "synthetic-moderator", "device-phase5-moderator-001")
	responder := verticalLoginProvider(t, client, server.URL, "synthetic-emergency", "device-phase5-responder-001")
	operations := verticalLoginProvider(t, client, server.URL, "synthetic-ops-admin", "device-phase5-ops-001")

	mediaResponse := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/media", `{"asset_id":"asset-phase5-story-001","kind":"VIDEO"}`, customer, map[string]string{"Idempotency-Key": "phase5-media-create-001"})
	assertVerticalResponse(t, mediaResponse, http.StatusAccepted, `"state":"QUARANTINED"`)
	var media struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, mediaResponse, &media)
	processed := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/moderation/media/"+media.ID+"/process", `{"clean":true}`, moderator, map[string]string{"Idempotency-Key": "phase5-media-process-001"})
	assertVerticalResponse(t, processed, http.StatusOK, `"state":"READY"`, `"transcode_status":"COMPLETE"`)
	story := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/ephemeral", `{"kind":"STORY","media_job_id":"`+media.ID+`","caption":"Local community update"}`, customer, map[string]string{"Idempotency-Key": "phase5-story-create-001"})
	assertVerticalResponse(t, story, http.StatusCreated, `"kind":"STORY"`, `"status":"PUBLISHED"`)

	opened := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/conversations", `{"profile_id":"customer-public-synthetic-001"}`, customer, map[string]string{"Idempotency-Key": "phase5-conversation-open-001"})
	assertVerticalResponse(t, opened, http.StatusCreated, `"status":"REQUESTED"`)
	var conversation struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, opened, &conversation)
	accepted := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/conversations/"+conversation.ID+"/accept", "", publicCustomer, map[string]string{"Idempotency-Key": "phase5-conversation-accept-001"})
	assertVerticalResponse(t, accepted, http.StatusOK, `"status":"ACCEPTED"`)
	message := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/social/conversations/"+conversation.ID+"/messages", `{"body":"Hello from the complete Phase 5 journey"}`, customer, map[string]string{"Idempotency-Key": "phase5-message-send-001"})
	assertVerticalResponse(t, message, http.StatusCreated, `"status":"DELIVERED"`)

	home := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/homes/listings", `{"title":"Modern family apartment","property_type":"APARTMENT","purpose":"SALE","locality":"Adyar Chennai","latitude":13.0012,"longitude":80.2565,"area_sq_ft":1400,"bedrooms":3,"price":{"amount_minor":1750000000,"currency":"INR"},"amenities":["parking"],"media_asset_ids":["asset-home-phase5-001"]}`, customer, map[string]string{"Idempotency-Key": "phase5-home-create-001"})
	assertVerticalResponse(t, home, http.StatusCreated, `"status":"DRAFT"`, `"kyc_verified":true`)
	var homeValue struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, home, &homeValue)
	published := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/homes/listings/"+homeValue.ID+"/publish", "", customer, mutationHeaders("phase5-home-publish-001", homeValue.Revision))
	assertVerticalResponse(t, published, http.StatusOK, `"status":"ACTIVE"`, `"version":"homes-avm-2026.08"`)
	homes := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/homes/listings?locality=Adyar&property_type=APARTMENT", "", customer)
	assertVerticalResponse(t, homes, http.StatusOK, `"id":"home-listing-synthetic-001"`, `"id":"`+homeValue.ID+`"`)

	classified := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/classifieds/listings/classified-listing-synthetic-001", "", customer)
	assertVerticalResponse(t, classified, http.StatusOK, `"contact_masked":"********3210"`)
	contact := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/classifieds/listings/classified-listing-synthetic-001/contact", `{"channel":"WHATSAPP","consent":true}`, customer)
	assertVerticalResponse(t, contact, http.StatusOK, `"contact":"919876543210"`)

	emergencyResponse := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/emergency/requests", `{"category":"MEDICAL","description":"Neighbour needs urgent assistance","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.03,"longitude":80.27,"accuracy_m":12,"captured_at":"2026-08-29T10:00:00Z"}}`, customer, map[string]string{"Idempotency-Key": "phase5-emergency-create-001"})
	assertVerticalResponse(t, emergencyResponse, http.StatusCreated, `"status":"OPEN"`, `"location_consent":true`)
	var emergencyValue struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, emergencyResponse, &emergencyValue)
	assigned := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/emergency/requests/"+emergencyValue.ID+"/accept", "", responder, map[string]string{"Idempotency-Key": "phase5-emergency-accept-001"})
	assertVerticalResponse(t, assigned, http.StatusOK, `"status":"ASSIGNED"`, `"assigned_responder":"emergency-responder-001"`)

	dashboard := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/governance/dashboard", "", operations)
	assertVerticalResponse(t, dashboard, http.StatusOK, `"privacy_mode":"aggregate_and_masked"`, `"title":"Emergency SLA"`, `"precision":"aggregate_region"`, `"pii_masked":true`)
}
