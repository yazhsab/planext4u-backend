package verticalslice

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestBEP4012GatewayVendorOnboardingCatalogAndFirstSettlement(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)
	application, err := New(Config{SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"), Clock: func() time.Time { return now }, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	client := server.Client()
	vendor := verticalLoginProvider(t, client, server.URL, "synthetic-vendor", "device-vendor-e2e-001")
	admin := verticalLoginProvider(t, client, server.URL, "synthetic-ops-admin", "device-admin-e2e-001")
	field := verticalLoginProvider(t, client, server.URL, "synthetic-field-officer", "device-field-e2e-001")
	finance := verticalLoginProvider(t, client, server.URL, "synthetic-finance-one", "device-finance-e2e-001")

	registered := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/applications", `{"business_name":"Synthetic Home Services","business_type":"Home services","contact_name":"Synthetic Vendor"}`, vendor, map[string]string{"Idempotency-Key": "e2e-vendor-register-001"})
	assertVerticalResponse(t, registered, http.StatusCreated, `"status":"REGISTERED"`, `"documents":[]`, `"service_zones":[]`)
	var applicationPayload struct {
		Revision int64 `json:"revision"`
	}
	decodeVerticalJSON(t, registered, &applicationPayload)
	documents := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/application/documents", `{"documents":[{"kind":"BUSINESS_REGISTRATION","asset_id":"asset-vendor-business-e2e"},{"kind":"OWNER_IDENTITY","asset_id":"asset-vendor-owner-e2e"}]}`, vendor, mutationHeaders("e2e-vendor-documents-01", applicationPayload.Revision))
	assertVerticalResponse(t, documents, http.StatusOK, `"status":"DOCUMENTS_SUBMITTED"`)
	decodeVerticalJSON(t, documents, &applicationPayload)
	for _, transition := range []struct{ status, reason, key string }{
		{"OCR_REVIEW", "OCR extraction completed", "e2e-vendor-ocr-review-01"},
		{"KYC_REVIEW", "Documents manually verified", "e2e-vendor-kyc-review-01"},
		{"FIELD_VISIT_REQUIRED", "Physical verification required", "e2e-vendor-field-required"},
	} {
		body, _ := json.Marshal(map[string]string{"status": transition.status, "reason": transition.reason})
		response := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/applications/vendor-synthetic-001/transitions", string(body), admin, mutationHeaders(transition.key, applicationPayload.Revision))
		assertVerticalResponse(t, response, http.StatusOK, `"status":"`+transition.status+`"`)
		decodeVerticalJSON(t, response, &applicationPayload)
	}
	visitAt := now.Add(24 * time.Hour).Format(time.RFC3339)
	visit := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/application/field-visit", `{"scheduled_at":"`+visitAt+`","latitude":13.0827,"longitude":80.2707,"allowed_radius_m":200}`, vendor, mutationHeaders("e2e-vendor-visit-schedule", applicationPayload.Revision))
	assertVerticalResponse(t, visit, http.StatusOK, `"status":"FIELD_VISIT_SCHEDULED"`)
	decodeVerticalJSON(t, visit, &applicationPayload)
	checkIn := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/applications/vendor-synthetic-001/field-check-in", `{"latitude":13.08271,"longitude":80.27071}`, field, mutationHeaders("e2e-vendor-field-checkin", applicationPayload.Revision))
	assertVerticalResponse(t, checkIn, http.StatusOK, `"status":"FIELD_VISIT_PASSED"`)
	decodeVerticalJSON(t, checkIn, &applicationPayload)
	zones := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/application/zones", `{"zones":[{"id":"zone-chennai-core","postal_codes":["600001"],"latitude":13.0827,"longitude":80.2707,"radius_km":25,"policy_version":"zone-policy-v1"}]}`, vendor, mutationHeaders("e2e-vendor-zones-command", applicationPayload.Revision))
	assertVerticalResponse(t, zones, http.StatusOK, `"zone-chennai-core"`)
	decodeVerticalJSON(t, zones, &applicationPayload)
	bank := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/application/bank", `{"reference":"bankref_vendor_e2e001","holder_name":"Synthetic Vendor","last4":"1234","ifsc":"HDFC0001234"}`, vendor, mutationHeaders("e2e-vendor-bank-command1", applicationPayload.Revision))
	assertVerticalResponse(t, bank, http.StatusOK, `"status":"BANK_REVIEW"`, `"status":"PENDING_VERIFICATION"`)
	decodeVerticalJSON(t, bank, &applicationPayload)
	verified := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/applications/vendor-synthetic-001/bank-verification", `{"reason":"Penny-drop account match passed"}`, finance, mutationHeaders("e2e-vendor-bank-verify01", applicationPayload.Revision))
	assertVerticalResponse(t, verified, http.StatusOK, `"status":"VERIFIED"`)
	decodeVerticalJSON(t, verified, &applicationPayload)
	approved := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/applications/vendor-synthetic-001/transitions", `{"status":"APPROVED","reason":"All onboarding controls passed"}`, admin, mutationHeaders("e2e-vendor-final-approve", applicationPayload.Revision))
	assertVerticalResponse(t, approved, http.StatusOK, `"status":"APPROVED"`, `"verified":true`)

	catalog := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/catalog", `{"kind":"SERVICE","name":"Deep cleaning","description":"Verified two-person home cleaning","sku":"CLEAN-DEEP-001","price":{"amount_minor":20000,"currency":"INR"}}`, vendor, map[string]string{"Idempotency-Key": "e2e-vendor-catalog-create"})
	assertVerticalResponse(t, catalog, http.StatusCreated, `"approval_status":"PENDING_APPROVAL"`)
	var catalogPayload struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, catalog, &catalogPayload)
	inventory := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/catalog/"+catalogPayload.ID+"/inventory", `{"stock":4}`, vendor, mutationHeaders("e2e-vendor-inventory-set", catalogPayload.Revision))
	assertVerticalResponse(t, inventory, http.StatusOK, `"stock":4`)
	decodeVerticalJSON(t, inventory, &catalogPayload)
	catalogApproved := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/vendor/catalog/"+catalogPayload.ID+"/approval", `{"approved":true,"reason":"Catalog policy checks passed"}`, admin, mutationHeaders("e2e-vendor-catalog-approve", catalogPayload.Revision))
	assertVerticalResponse(t, catalogApproved, http.StatusOK, `"approval_status":"APPROVED"`, `"active":true`)
	dashboard := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/vendor/dashboard", "", vendor)
	assertVerticalResponse(t, dashboard, http.StatusOK, `"catalog_items":1`, `"low_stock_items":1`)
}

func TestBEP4012GatewayFoodRiderChatDeliveryAndPayout(t *testing.T) {
	clock := time.Date(2026, 8, 28, 10, 30, 0, 0, time.UTC)
	application, err := New(Config{SigningKey: []byte("synthetic-staging-key-32-bytes-minimum-value"), Clock: func() time.Time { return clock }, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application)
	t.Cleanup(server.Close)
	client := server.Client()
	customer := verticalLoginProvider(t, client, server.URL, "synthetic-customer", "device-customer-food-e2e")
	restaurant := verticalLoginProvider(t, client, server.URL, "synthetic-restaurant", "device-restaurant-e2e")
	rider := verticalLoginProvider(t, client, server.URL, "synthetic-rider", "device-rider-e2e-001")
	admin := verticalLoginProvider(t, client, server.URL, "synthetic-ops-admin", "device-admin-food-e2e")
	financeOne := verticalLoginProvider(t, client, server.URL, "synthetic-finance-one", "device-finance-food-1")
	financeTwo := verticalLoginProvider(t, client, server.URL, "synthetic-finance-two", "device-finance-food-2")

	restaurants := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/restaurants?postal_code=600001", "", customer)
	assertVerticalResponse(t, restaurants, http.StatusOK, `"id":"restaurant-saravana"`, `"verified":true`)
	cart := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/food-carts", `{"restaurant_id":"restaurant-saravana","postal_code":"600001","lines":[{"menu_item_id":"menu-meals-001","quantity":2,"option_ids":["option-meals-large","option-curd"],"note":"Less spicy"}]}`, customer, map[string]string{"Idempotency-Key": "e2e-food-cart-command01"})
	assertVerticalResponse(t, cart, http.StatusCreated, `"amount_minor":43450`, `"pricing_version":"food-pricing-v1"`)
	var cartPayload struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, cart, &cartPayload)
	created := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/food-orders", `{"cart_id":"`+cartPayload.ID+`","payment_method":"WALLET"}`, customer, map[string]string{"Idempotency-Key": "e2e-food-order-create01"})
	assertVerticalResponse(t, created, http.StatusCreated, `"status":"PENDING_RESTAURANT"`, `"status":"CAPTURED"`)
	var foodOrder struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, created, &foodOrder)
	for _, transition := range []struct{ status, key string }{{"ACCEPTED", "e2e-food-restaurant-accept"}, {"PREPARING", "e2e-food-restaurant-prep01"}, {"READY", "e2e-food-restaurant-ready1"}} {
		response := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/food-orders/"+foodOrder.ID+"/restaurant-transition", `{"status":"`+transition.status+`"}`, restaurant, mutationHeaders(transition.key, foodOrder.Revision))
		assertVerticalResponse(t, response, http.StatusOK, `"status":"`+transition.status+`"`)
		decodeVerticalJSON(t, response, &foodOrder)
	}

	riderApplication := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/applications", `{"full_name":"Synthetic Rider","phone_masked":"******1234","vehicle_type":"MOTORBIKE","vehicle_number":"TN01AB1234","documents":[{"kind":"DRIVER_LICENSE","asset_id":"asset-rider-license-e2e"},{"kind":"IDENTITY","asset_id":"asset-rider-identity-e2e"}],"bank_reference":"bankref_rider_e2e001","zones":["600001"]}`, rider, map[string]string{"Idempotency-Key": "e2e-rider-register-0001"})
	assertVerticalResponse(t, riderApplication, http.StatusCreated, `"status":"KYC_REVIEW"`)
	var riderProfile struct {
		Revision int64 `json:"revision"`
	}
	decodeVerticalJSON(t, riderApplication, &riderProfile)
	riderApproved := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/applications/rider-synthetic-001/review", `{"approved":true,"reason":"Rider KYC and bank verified"}`, admin, mutationHeaders("e2e-rider-review-command", riderProfile.Revision))
	assertVerticalResponse(t, riderApproved, http.StatusOK, `"status":"APPROVED"`)
	duty := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/duty/start", `{"zone_id":"600001"}`, rider, map[string]string{"Idempotency-Key": "e2e-rider-duty-start-01"})
	assertVerticalResponse(t, duty, http.StatusCreated, `"status":"ACTIVE"`)
	emergencyIncident := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/emergency-incidents", `{"category":"SAFETY","description":"Rider requested urgent assistance while on duty","priority":"CRITICAL","location_consent":true,"location":{"latitude":13.0827,"longitude":80.2707,"accuracy_m":8}}`, rider, map[string]string{"Idempotency-Key": "e2e-rider-emergency-0001"})
	assertVerticalResponse(t, emergencyIncident, http.StatusCreated, `"status":"OPEN"`, `"location_consent":true`)
	var incident struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, emergencyIncident, &incident)
	incidentStatus := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/rider/emergency-incidents/"+incident.ID, "", rider)
	assertVerticalResponse(t, incidentStatus, http.StatusOK, `"id":"`+incident.ID+`"`, `"status":"OPEN"`)
	revokedLocation := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/rider/emergency-incidents/"+incident.ID+"/location", `{"consent":false,"location":{"latitude":0,"longitude":0,"accuracy_m":0}}`, rider)
	assertVerticalResponse(t, revokedLocation, http.StatusOK, `"location_consent":false`)

	seedBody, _ := json.Marshal(map[string]any{"id": "delivery-food-e2e-001", "order_id": foodOrder.ID, "order_type": "FOOD", "region_id": "region-chennai", "territory_id": "territory-chennai-core", "zone_id": "600001", "pickup": map[string]any{"label": "Saravana Kitchen", "address_token": "address-token-pickup-e2e", "point": map[string]float64{"latitude": 13.0827, "longitude": 80.2707}}, "dropoff": map[string]any{"label": "Customer", "address_token": "address-token-dropoff-e2e", "point": map[string]float64{"latitude": 13.0674, "longitude": 80.2376}}, "distance_meters": 4800, "earning": map[string]any{"amount_minor": 8000, "currency": "INR"}, "delivery_otp": "135790", "customer_id": "customer-synthetic-001", "counterparty_id": "restaurant-owner-001"})
	taskCreated := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/dispatch/tasks", string(seedBody), admin)
	assertVerticalResponse(t, taskCreated, http.StatusCreated, `"status":"READY_FOR_DISPATCH"`)
	var task struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, taskCreated, &task)
	offered := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/dispatch/tasks/"+task.ID+"/offer", "", admin, mutationHeaders("e2e-dispatch-offer-task1", task.Revision))
	assertVerticalResponse(t, offered, http.StatusOK, `"status":"OFFERED"`)
	decodeVerticalJSON(t, offered, &task)
	declineSeedBody, _ := json.Marshal(map[string]any{"id": "delivery-food-decline-e2e", "order_id": "food-order-decline-e2e", "order_type": "FOOD", "region_id": "region-chennai", "territory_id": "territory-chennai-core", "zone_id": "600001", "pickup": map[string]any{"label": "Saravana Kitchen", "address_token": "address-token-pickup-decline", "point": map[string]float64{"latitude": 13.0827, "longitude": 80.2707}}, "dropoff": map[string]any{"label": "Customer", "address_token": "address-token-dropoff-decline", "point": map[string]float64{"latitude": 13.0674, "longitude": 80.2376}}, "distance_meters": 6200, "earning": map[string]any{"amount_minor": 9000, "currency": "INR"}, "delivery_otp": "246802", "customer_id": "customer-decline-e2e", "counterparty_id": "restaurant-owner-001"})
	declineTaskCreated := verticalRequest(t, client, http.MethodPost, server.URL+"/v1/dispatch/tasks", string(declineSeedBody), admin)
	assertVerticalResponse(t, declineTaskCreated, http.StatusCreated, `"status":"READY_FOR_DISPATCH"`)
	var declineTask struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, declineTaskCreated, &declineTask)
	declineOffered := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/dispatch/tasks/"+declineTask.ID+"/offer", "", admin, mutationHeaders("e2e-dispatch-decline-offer", declineTask.Revision))
	assertVerticalResponse(t, declineOffered, http.StatusOK, `"status":"OFFERED"`, `"DECLINE"`)
	decodeVerticalJSON(t, declineOffered, &declineTask)
	declined := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/offers/"+declineTask.ID+"/decline", `{"reason_code":"TOO_FAR"}`, rider, mutationHeaders("e2e-rider-decline-offer", declineTask.Revision))
	assertVerticalResponse(t, declined, http.StatusCreated, `"reason_code":"TOO_FAR"`, `"task_id":"`+declineTask.ID+`"`)
	offers := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/rider/offers", "", rider)
	assertVerticalResponse(t, offers, http.StatusOK, `"id":"`+task.ID+`"`)
	accepted := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/tasks/"+task.ID+"/accept", "", rider, mutationHeaders("e2e-rider-accept-offer1", task.Revision))
	assertVerticalResponse(t, accepted, http.StatusOK, `"status":"ASSIGNED"`)
	decodeVerticalJSON(t, accepted, &task)
	location := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/location", `{"sequence":1,"point":{"latitude":13.0827,"longitude":80.2707},"accuracy_m":8,"captured_at":"`+clock.Format(time.RFC3339)+`"}`, rider, map[string]string{"Idempotency-Key": "e2e-rider-location-0001"})
	assertVerticalResponse(t, location, http.StatusOK, `"sequence":1`)
	chat := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/order-chats/"+foodOrder.ID, "", customer)
	assertVerticalResponse(t, chat, http.StatusOK, `"rider-synthetic-001"`)
	var conversation struct {
		ID string `json:"id"`
	}
	decodeVerticalJSON(t, chat, &conversation)
	message := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/order-chats/"+conversation.ID+"/messages", `{"body":"Please call +91 98765 43210"}`, customer, map[string]string{"Idempotency-Key": "e2e-chat-send-command01"})
	assertVerticalResponse(t, message, http.StatusCreated, `"redacted":true`, `[contact redacted]`)
	picked := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/tasks/"+task.ID+"/pickup", "", rider, mutationHeaders("e2e-rider-pickup-task1", task.Revision))
	assertVerticalResponse(t, picked, http.StatusOK, `"status":"PICKED_UP"`)
	decodeVerticalJSON(t, picked, &task)
	delivered := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/rider/tasks/"+task.ID+"/completion", `{"otp":"135790","blurred_photo_asset_id":"asset-blurred-e2e-proof"}`, rider, mutationHeaders("e2e-rider-deliver-task", task.Revision))
	assertVerticalResponse(t, delivered, http.StatusOK, `"status":"DELIVERED"`, `"pod_blurred_asset_id":"asset-blurred-e2e-proof"`)

	ledger := verticalRequest(t, client, http.MethodGet, server.URL+"/v1/settlements/ledger", "", rider)
	assertVerticalResponse(t, ledger, http.StatusOK, `"calculation_version":"rider-commission-v1"`, `"amount_minor":7056`)
	var ledgerPayload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeVerticalJSON(t, ledger, &ledgerPayload)
	clock = clock.Add(49 * time.Hour)
	rider = verticalLoginProvider(t, client, server.URL, "synthetic-rider", "device-rider-payout-e2e")
	financeOne = verticalLoginProvider(t, client, server.URL, "synthetic-finance-one", "device-finance-payout-1")
	financeTwo = verticalLoginProvider(t, client, server.URL, "synthetic-finance-two", "device-finance-payout-2")
	payout := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/payouts", `{"entry_ids":["`+ledgerPayload.Items[0].ID+`"]}`, rider, map[string]string{"Idempotency-Key": "e2e-rider-payout-request"})
	assertVerticalResponse(t, payout, http.StatusCreated, `"status":"PENDING_REVIEW"`, `"amount_minor":7056`)
	var payoutPayload struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	decodeVerticalJSON(t, payout, &payoutPayload)
	first := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/payouts/"+payoutPayload.ID+"/approve", `{"reason":"First payout control approved"}`, financeOne, mutationHeaders("e2e-payout-first-approve", payoutPayload.Revision))
	assertVerticalResponse(t, first, http.StatusOK, `"status":"FIRST_APPROVED"`)
	decodeVerticalJSON(t, first, &payoutPayload)
	second := verticalRequestWithHeaders(t, client, http.MethodPost, server.URL+"/v1/payouts/"+payoutPayload.ID+"/approve", `{"reason":"Second payout control approved"}`, financeTwo, mutationHeaders("e2e-payout-second-appr", payoutPayload.Revision))
	assertVerticalResponse(t, second, http.StatusOK, `"status":"APPROVED"`)
}

func verticalLoginProvider(t *testing.T, client *http.Client, baseURL, providerToken, deviceID string) string {
	t.Helper()
	response := verticalRequest(t, client, http.MethodPost, baseURL+"/v1/auth/exchange", `{"provider":"local","provider_token":"`+providerToken+`","device_id":"`+deviceID+`","country":"IN"}`, "")
	assertVerticalResponse(t, response, http.StatusCreated)
	var payload struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	decodeVerticalJSON(t, response, &payload)
	return payload.Tokens.AccessToken
}

func mutationHeaders(key string, revision int64) map[string]string {
	return map[string]string{"Idempotency-Key": key, "If-Match": `"` + strconv.FormatInt(revision, 10) + `"`}
}
