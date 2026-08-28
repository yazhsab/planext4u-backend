package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRazorpayProviderCreatesServerOwnedOrderAndPublicHandoff(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Authorization"), "Basic ") {
			t.Errorf("authorization was not Basic")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/orders":
			var input struct {
				Amount   int64             `json:"amount"`
				Currency string            `json:"currency"`
				Receipt  string            `json:"receipt"`
				Notes    map[string]string `json:"notes"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			if input.Amount != 14785 || input.Currency != "INR" || input.Receipt != "checkout-provider-001" || input.Notes["planext4u_payment_id"] == "" {
				t.Errorf("input = %#v", input)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"order_provider_001","entity":"order","amount":14785,"currency":"INR","status":"created"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/payments/pay_provider_001":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"pay_provider_001","order_id":"order_provider_001","amount":14785,"currency":"INR","status":"captured"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/payments/pay_provider_001/refund":
			var input struct {
				Amount int64             `json:"amount"`
				Notes  map[string]string `json:"notes"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			if input.Amount != 4785 || input.Notes["planext4u_payment_id"] == "" {
				t.Errorf("refund input = %#v", input)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"rfnd_provider_001","entity":"refund","payment_id":"pay_provider_001","amount":4785,"currency":"INR","status":"pending"}`))
		default:
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	base, _ := url.Parse(provider.URL)
	adapter, err := NewRazorpayProvider(HTTPProviderConfig{BaseURL: base, Client: provider.Client(), PublicKey: "rzp_test_public_001", Secret: []byte("provider-secret-value"), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithProviders(paymentClock, map[Method][]byte{MethodRazorpay: []byte("webhook-secret-32-bytes-minimum-value")}, map[Method]ProviderInitializer{MethodRazorpay: adapter})
	if err != nil {
		t.Fatal(err)
	}
	created, replayed, err := service.CreateWithPayer(context.Background(), paymentScope(), "idem-provider-create-0001", "checkout-provider-001", MethodRazorpay, Money{AmountMinor: 14785, Currency: "INR"}, Payer{Email: "buyer@example.com"})
	if err != nil || replayed || created.ClientHandoff == nil || created.ClientHandoff.PublicKey != "rzp_test_public_001" || created.ClientHandoff.ProviderOrderID != "order_provider_001" {
		t.Fatalf("created = %#v, replayed=%v err=%v", created, replayed, err)
	}
	if strings.Contains(string(mustJSON(t, created)), "provider-secret-value") {
		t.Fatal("provider secret crossed the API model")
	}
	body := mustJSON(t, map[string]any{
		"event": "payment.captured",
		"payload": map[string]any{"payment": map[string]any{"entity": map[string]any{
			"id": "pay_provider_001", "order_id": created.ProviderReference,
			"status": "captured", "amount": int64(14785), "currency": "INR",
			"notes": map[string]string{"planext4u_payment_id": created.ID},
		}}},
	})
	webhookSecret := []byte("webhook-secret-32-bytes-minimum-value")
	if _, _, err = service.HandleProviderWebhook(MethodRazorpay, Sign(webhookSecret, body), "provider-event-verify-001", body); err != nil {
		t.Fatal(err)
	}
	reconciled, err := service.ReconcileWithProvider(context.Background(), paymentScope(), created.ID)
	if err != nil || reconciled.Status != StatusReconciled || reconciled.ProviderTransactionReference != "pay_provider_001" {
		t.Fatalf("reconciled = %#v err=%v", reconciled, err)
	}
	refunded, err := service.RequestRefundWithProvider(context.Background(), paymentScope(), created.ID, Money{AmountMinor: 4785, Currency: "INR"}, "Partial returned line")
	if err != nil || refunded.Status != StatusRefundSubmitted || refunded.ProviderRefundReference != "rfnd_provider_001" {
		t.Fatalf("refund = %#v err=%v", refunded, err)
	}
}

func TestPaystackProviderAndNativeSHA512Webhook(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer paystack-secret-value" {
			t.Errorf("unexpected provider request")
		}
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/transaction/initialize":
			_, _ = writer.Write([]byte(`{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.paystack.com/access_provider_001","access_code":"access_provider_001","reference":"checkout-provider-ng-001"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/transaction/verify/checkout-provider-ng-001":
			_, _ = writer.Write([]byte(`{"status":true,"message":"Verification successful","data":{"id":987654,"reference":"checkout-provider-ng-001","status":"success","amount":500000,"currency":"NGN"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/refund":
			var input struct {
				Transaction string `json:"transaction"`
				Amount      int64  `json:"amount"`
				Currency    string `json:"currency"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			if input.Transaction != "checkout-provider-ng-001" || input.Amount != 125000 || input.Currency != "NGN" {
				t.Errorf("refund input = %#v", input)
			}
			_, _ = writer.Write([]byte(`{"status":true,"message":"Refund has been queued","data":{"id":765432,"status":"pending","amount":125000,"currency":"NGN","transaction":{"reference":"checkout-provider-ng-001"}}}`))
		default:
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	base, _ := url.Parse(provider.URL)
	adapter, err := NewPaystackProvider(HTTPProviderConfig{BaseURL: base, Client: provider.Client(), PublicKey: "pk_test_public_001", Secret: []byte("paystack-secret-value"), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	webhookSecret := []byte("paystack-webhook-secret-32-bytes-value")
	service, err := NewServiceWithProviders(paymentClock, map[Method][]byte{MethodPaystack: webhookSecret}, map[Method]ProviderInitializer{MethodPaystack: adapter})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: "tenant-synthetic-001", Country: "NG", CustomerID: "customer-synthetic-001"}
	created, _, err := service.CreateWithPayer(context.Background(), scope, "idem-provider-create-ng-0001", "checkout-provider-ng-001", MethodPaystack, Money{AmountMinor: 500000, Currency: "NGN"}, Payer{Email: "buyer@example.com"})
	if err != nil || created.ClientHandoff == nil || created.ClientHandoff.AccessCode != "access_provider_001" {
		t.Fatalf("created = %#v err=%v", created, err)
	}
	body := mustJSON(t, map[string]any{
		"event": "charge.success",
		"data":  map[string]any{"id": 987654, "reference": created.ProviderReference, "status": "success", "amount": int64(500000), "currency": "NGN", "metadata": map[string]string{"planext4u_payment_id": created.ID}},
	})
	mac := hmac.New(sha512.New, webhookSecret)
	_, _ = mac.Write(body)
	captured, duplicate, err := service.HandleProviderWebhook(MethodPaystack, hex.EncodeToString(mac.Sum(nil)), "", body)
	if err != nil || duplicate || captured.Status != StatusCaptured {
		t.Fatalf("captured = %#v duplicate=%v err=%v", captured, duplicate, err)
	}
	replayed, duplicate, err := service.HandleProviderWebhook(MethodPaystack, hex.EncodeToString(mac.Sum(nil)), "", body)
	if err != nil || !duplicate || replayed.ID != captured.ID {
		t.Fatalf("replayed = %#v duplicate=%v err=%v", replayed, duplicate, err)
	}
	reconciled, err := service.ReconcileWithProvider(context.Background(), scope, created.ID)
	if err != nil || reconciled.Status != StatusReconciled || reconciled.ProviderTransactionReference != "paystack-987654" {
		t.Fatalf("reconciled = %#v err=%v", reconciled, err)
	}
	refunded, err := service.RequestRefundWithProvider(context.Background(), scope, created.ID, Money{AmountMinor: 125000, Currency: "NGN"}, "Partial returned line")
	if err != nil || refunded.Status != StatusRefundSubmitted || refunded.ProviderRefundReference != "paystack-refund-765432" {
		t.Fatalf("refund = %#v err=%v", refunded, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
