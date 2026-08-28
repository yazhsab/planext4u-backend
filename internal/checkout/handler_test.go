package checkout

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
)

func TestPhase3CustomerTransactionAPI(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	handler, _ := NewHandler(fixture.service)

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1/wallet", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("untrusted wallet = %d %s", denied.Code, denied.Body.String())
	}

	quoteRequest := httptest.NewRequest(http.MethodPost, "/v1/checkout/quotes", strings.NewReader(`{"cart_revision":1,"address_id":"address-home-001","delivery_slot_id":"slot-standard-001","promotion_code":"LOCAL10","wallet_points":400}`))
	setCheckoutCustomer(quoteRequest)
	quoteRequest.Header.Set("Idempotency-Key", "idem-handler-quote-0001")
	quoteResponse := httptest.NewRecorder()
	handler.ServeHTTP(quoteResponse, quoteRequest)
	var quote Quote
	_ = json.Unmarshal(quoteResponse.Body.Bytes(), &quote)
	if quoteResponse.Code != http.StatusCreated || quote.ID == "" || quote.Total.AmountMinor != 20000 {
		t.Fatalf("quote = %d %s", quoteResponse.Code, quoteResponse.Body.String())
	}

	placeBody, _ := json.Marshal(map[string]any{"quote_id": quote.ID, "payment_method": payment.MethodRazorpay})
	placeRequest := httptest.NewRequest(http.MethodPost, "/v1/checkout/orders", bytes.NewReader(placeBody))
	setCheckoutCustomer(placeRequest)
	placeRequest.Header.Set("Idempotency-Key", "idem-handler-place-0001")
	placeResponse := httptest.NewRecorder()
	handler.ServeHTTP(placeResponse, placeRequest)
	var placed PlaceResult
	_ = json.Unmarshal(placeResponse.Body.Bytes(), &placed)
	if placeResponse.Code != http.StatusCreated || placed.Order.Status != order.StatusPendingPayment {
		t.Fatalf("place = %d %s", placeResponse.Code, placeResponse.Body.String())
	}

	eventBody, _ := json.Marshal(map[string]any{
		"event": "payment.captured",
		"payload": map[string]any{"payment": map[string]any{"entity": map[string]any{
			"id": "pay-handler-provider-001", "order_id": placed.Payment.ProviderReference,
			"status": "captured", "amount": quote.Total.AmountMinor, "currency": quote.Total.Currency,
			"notes": map[string]string{"planext4u_payment_id": placed.Payment.ID},
		}}},
	})
	webhookRequest := httptest.NewRequest(http.MethodPost, "/v1/payments/webhooks/razorpay", bytes.NewReader(eventBody))
	webhookRequest.Header.Set("X-Razorpay-Signature", payment.Sign(fixture.secret, eventBody))
	webhookRequest.Header.Set("X-Razorpay-Event-Id", "handler-provider-event-001")
	webhookResponse := httptest.NewRecorder()
	handler.ServeHTTP(webhookResponse, webhookRequest)
	if webhookResponse.Code != http.StatusOK {
		t.Fatalf("webhook = %d %s", webhookResponse.Code, webhookResponse.Body.String())
	}

	orderRequest := httptest.NewRequest(http.MethodGet, "/v1/orders/"+placed.Order.ID, nil)
	setCheckoutCustomer(orderRequest)
	orderResponse := httptest.NewRecorder()
	handler.ServeHTTP(orderResponse, orderRequest)
	if orderResponse.Code != http.StatusOK || orderResponse.Header().Get("ETag") != strconv.Quote("2") || !strings.Contains(orderResponse.Body.String(), `"status":"PLACED"`) {
		t.Fatalf("order = %d headers=%v body=%s", orderResponse.Code, orderResponse.Header(), orderResponse.Body.String())
	}

	walletRequest := httptest.NewRequest(http.MethodGet, "/v1/wallet", nil)
	setCheckoutCustomer(walletRequest)
	walletResponse := httptest.NewRecorder()
	handler.ServeHTTP(walletResponse, walletRequest)
	if walletResponse.Code != http.StatusOK || !strings.Contains(walletResponse.Body.String(), `"balance":600`) {
		t.Fatalf("wallet = %d %s", walletResponse.Code, walletResponse.Body.String())
	}
}

func TestPhase3CustomerAddressAPI(t *testing.T) {
	t.Parallel()
	fixture := newCheckoutFixture(t, WalletHybrid)
	handler, _ := NewHandler(fixture.service)

	create := httptest.NewRequest(http.MethodPost, "/v1/addresses", strings.NewReader(`{"label":"Work","line1":"42 Commerce Road","postal_code":"641001","locality":"Coimbatore","default":false}`))
	setCheckoutCustomer(create)
	create.Header.Set("Idempotency-Key", "idem-handler-address-create-0001")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	var created Address
	_ = json.Unmarshal(createdResponse.Body.Bytes(), &created)
	if createdResponse.Code != http.StatusCreated || created.ID == "" || createdResponse.Header().Get("ETag") != strconv.Quote("1") {
		t.Fatalf("create address = %d headers=%v body=%s", createdResponse.Code, createdResponse.Header(), createdResponse.Body.String())
	}

	update := httptest.NewRequest(http.MethodPatch, "/v1/addresses/"+created.ID, strings.NewReader(`{"label":"Work","line1":"84 Commerce Road","postal_code":"641001","locality":"Coimbatore","default":true}`))
	setCheckoutCustomer(update)
	update.Header.Set("Idempotency-Key", "idem-handler-address-update-0001")
	update.Header.Set("If-Match", strconv.Quote("1"))
	updatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(updatedResponse, update)
	if updatedResponse.Code != http.StatusOK || updatedResponse.Header().Get("ETag") != strconv.Quote("2") || !strings.Contains(updatedResponse.Body.String(), `"default":true`) {
		t.Fatalf("update address = %d headers=%v body=%s", updatedResponse.Code, updatedResponse.Header(), updatedResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/v1/addresses/"+created.ID, nil)
	setCheckoutCustomer(deleteRequest)
	deleteRequest.Header.Set("Idempotency-Key", "idem-handler-address-delete-0001")
	deleteRequest.Header.Set("If-Match", strconv.Quote("2"))
	deletedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deletedResponse, deleteRequest)
	if deletedResponse.Code != http.StatusNoContent {
		t.Fatalf("delete address = %d %s", deletedResponse.Code, deletedResponse.Body.String())
	}
}

func setCheckoutCustomer(request *http.Request) {
	request.Header.Set("X-Planext4u-Tenant", "tenant-synthetic-001")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-synthetic-001")
	request.Header.Set("X-Planext4u-Roles", "CUSTOMER")
	request.Header.Set("X-Correlation-ID", "corr-checkout-test")
}
