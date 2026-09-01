//go:build integration

package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPaymentProviderLifecycleAndExactlyOnceWebhooks(t *testing.T) {
	databaseURL := os.Getenv("PAYMENT_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("PAYMENT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS payment CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS payment CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/payment/000001_payment.up.sql",
		"../../migrations/payment/000002_provider_handoff.up.sql",
		"../../migrations/payment/000003_durable_provider_operations.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	secret := []byte("postgres-razorpay-webhook-secret-value")
	provider := &postgresPaymentProvider{}
	service, err := NewPostgresService(pool, func() time.Time { return now }, map[Method][]byte{MethodRazorpay: secret}, map[Method]ProviderInitializer{MethodRazorpay: provider})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: postgresPaymentTenantOne, Country: "IN", CustomerID: postgresPaymentCustomerOne}
	created, replay, err := service.CreateWithPayer(ctx, scope, "payment-create-postgres-001", "checkout-postgres-001", MethodRazorpay, Money{AmountMinor: 27500, Currency: "INR"}, Payer{Name: "P4U Customer", Phone: "+919000000000"})
	if err != nil || replay || created.Status != StatusProviderOrderCreated || created.ClientHandoff == nil || provider.InitializeCalls() != 1 {
		t.Fatalf("created=%#v replay=%t calls=%d err=%v", created, replay, provider.InitializeCalls(), err)
	}
	restarted, _ := NewPostgresService(pool, func() time.Time { return now }, map[Method][]byte{MethodRazorpay: secret}, map[Method]ProviderInitializer{MethodRazorpay: provider})
	replayed, replay, err := restarted.CreateWithPayer(ctx, scope, "payment-create-postgres-001", "checkout-postgres-001", MethodRazorpay, Money{AmountMinor: 27500, Currency: "INR"}, Payer{})
	if err != nil || !replay || replayed.ID != created.ID || provider.InitializeCalls() != 1 {
		t.Fatalf("replayed=%#v replay=%t calls=%d err=%v", replayed, replay, provider.InitializeCalls(), err)
	}
	if _, _, err := restarted.CreateWithPayer(ctx, scope, "payment-create-postgres-001", "checkout-postgres-001", MethodRazorpay, Money{AmountMinor: 28000, Currency: "INR"}, Payer{}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("create conflict=%v", err)
	}
	reconcilePayment, _, err := restarted.CreateWithPayer(ctx, scope, "payment-reconcile-postgres-1", "checkout-reconcile-postgres-1", MethodRazorpay, Money{AmountMinor: 19000, Currency: "INR"}, Payer{})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	candidates, err := restarted.ReconciliationCandidates(ctx, time.Minute, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("reconciliation candidates=%#v err=%v", candidates, err)
	}
	reconciled, err := restarted.ReconcileWithProvider(ctx, scope, reconcilePayment.ID)
	if err != nil || reconciled.Status != StatusReconciled || reconciled.ProviderTransactionReference == "" {
		t.Fatalf("reconciled=%#v err=%v", reconciled, err)
	}

	capturedBody := razorpayPaymentEvent(t, "payment.captured", created.ID, created.ProviderReference, "pay_postgres_001", 27500)
	captured, replay, err := restarted.HandleProviderWebhook(MethodRazorpay, signRazorpay(secret, capturedBody), "event-postgres-captured-001", capturedBody)
	if err != nil || replay || captured.Status != StatusCaptured || captured.ProviderTransactionReference != "pay_postgres_001" {
		t.Fatalf("captured=%#v replay=%t err=%v", captured, replay, err)
	}
	replayedEvent, replay, err := restarted.HandleProviderWebhook(MethodRazorpay, signRazorpay(secret, capturedBody), "event-postgres-captured-001", capturedBody)
	if err != nil || !replay || replayedEvent.Status != StatusCaptured {
		t.Fatalf("event replay=%#v replay=%t err=%v", replayedEvent, replay, err)
	}
	changedBody := razorpayPaymentEvent(t, "payment.captured", created.ID, created.ProviderReference, "pay_postgres_001", 27000)
	if _, _, err := restarted.HandleProviderWebhook(MethodRazorpay, signRazorpay(secret, changedBody), "event-postgres-captured-001", changedBody); !errors.Is(err, ErrProviderEventReuse) {
		t.Fatalf("event reuse=%v", err)
	}
	mismatchBody := razorpayPaymentEvent(t, "payment.captured", created.ID, created.ProviderReference, "pay_postgres_001", 27000)
	if _, _, err := restarted.HandleProviderWebhook(MethodRazorpay, signRazorpay(secret, mismatchBody), "event-postgres-mismatch-001", mismatchBody); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("reconciliation mismatch=%v", err)
	}
	var exceptions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment.reconciliation_exceptions WHERE payment_id=$1`, created.ID).Scan(&exceptions); err != nil || exceptions != 1 {
		t.Fatalf("reconciliation exceptions=%d err=%v", exceptions, err)
	}

	refunded, err := restarted.RequestRefundWithProvider(ctx, scope, created.ID, Money{AmountMinor: 5000, Currency: "INR"}, "Approved partial return")
	if err != nil || refunded.Status != StatusRefundSubmitted || refunded.ProviderRefundReference == "" || provider.RefundCalls() != 1 {
		t.Fatalf("refund=%#v calls=%d err=%v", refunded, provider.RefundCalls(), err)
	}
	refundBody := razorpayRefundEvent(t, created.ID, captured.ProviderTransactionReference, 5000)
	refunded, replay, err = restarted.HandleProviderWebhook(MethodRazorpay, signRazorpay(secret, refundBody), "event-postgres-refund-0001", refundBody)
	if err != nil || replay || refunded.Status != StatusRefunded {
		t.Fatalf("refund event=%#v replay=%t err=%v", refunded, replay, err)
	}

	cod, replay, err := restarted.CreateWithPayer(ctx, scope, "payment-cod-postgres-0001", "checkout-cod-postgres-001", MethodCOD, Money{AmountMinor: 1000, Currency: "INR"}, Payer{})
	if err != nil || replay || cod.Status != StatusAuthorisationPending {
		t.Fatalf("cod=%#v replay=%t err=%v", cod, replay, err)
	}
	cod, err = restarted.MarkCODCollected(scope, cod.ID)
	if err != nil || cod.Status != StatusCaptured {
		t.Fatalf("cod collected=%#v err=%v", cod, err)
	}
	other := Scope{TenantID: postgresPaymentTenantTwo, Country: "IN", CustomerID: postgresPaymentCustomerOne}
	if _, err := restarted.Get(other, created.ID); !errors.Is(err, ErrPaymentNotFound) {
		t.Fatalf("cross tenant get=%v", err)
	}
}

type postgresPaymentProvider struct {
	mu              sync.Mutex
	initializeCalls int
	refundCalls     int
}

func (provider *postgresPaymentProvider) Initialize(_ context.Context, input ProviderInitialization) (ProviderSession, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.initializeCalls++
	reference := "order_provider_" + input.PaymentID[:8]
	return ProviderSession{ProviderReference: reference, ClientHandoff: ClientHandoff{
		Type: "RAZORPAY_CHECKOUT", PublicKey: "rzp_test_postgres_public_key", ProviderOrderID: reference,
	}}, nil
}

func (provider *postgresPaymentProvider) Refund(_ context.Context, input ProviderRefundRequest) (ProviderRefundSubmission, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.refundCalls++
	return ProviderRefundSubmission{ProviderRefundReference: "refund_provider_" + input.PaymentID[:8], Status: StatusRefundSubmitted}, nil
}

func (provider *postgresPaymentProvider) Verify(_ context.Context, value Payment) (ProviderVerification, error) {
	return ProviderVerification{
		ProviderReference: value.ProviderReference, ProviderTransactionReference: "pay_verified_" + value.ID[:8],
		Status: StatusCaptured, Amount: value.Amount,
	}, nil
}

func (provider *postgresPaymentProvider) InitializeCalls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.initializeCalls
}

func (provider *postgresPaymentProvider) RefundCalls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.refundCalls
}

func razorpayPaymentEvent(t *testing.T, event, paymentID, orderID, transactionID string, amount int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"event": event,
		"payload": map[string]any{"payment": map[string]any{"entity": map[string]any{
			"id": transactionID, "order_id": orderID, "status": "captured", "amount": amount,
			"currency": "INR", "notes": map[string]string{"planext4u_payment_id": paymentID},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func razorpayRefundEvent(t *testing.T, paymentID, providerPaymentID string, amount int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"event": "refund.processed",
		"payload": map[string]any{"refund": map[string]any{"entity": map[string]any{
			"id": "refund_provider_event_001", "payment_id": providerPaymentID, "status": "processed",
			"amount": amount, "currency": "INR", "notes": map[string]string{"planext4u_payment_id": paymentID},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signRazorpay(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

const (
	postgresPaymentTenantOne   = "aebdb0c8-6c91-4fa3-90eb-fc5ace06a70b"
	postgresPaymentTenantTwo   = "4eb06960-2c93-4377-bda1-a06226acc7ea"
	postgresPaymentCustomerOne = "11482e2a-bf50-41a6-9392-160cbce510c7"
)
