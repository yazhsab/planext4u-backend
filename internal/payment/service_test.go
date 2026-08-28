package payment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSignedWebhookDuplicateAndReconciliation(t *testing.T) {
	t.Parallel()
	secret := []byte("synthetic-provider-secret-32-bytes-minimum")
	service, _ := NewService(paymentClock, map[Method][]byte{MethodRazorpay: secret})
	scope := paymentScope()
	created, replayed, err := service.Create(scope, "idem-payment-create-0001", "order-payment-001", MethodRazorpay, Money{AmountMinor: 12300, Currency: "INR"})
	if err != nil || replayed || created.Status != StatusProviderOrderCreated {
		t.Fatalf("created=%#v replayed=%v err=%v", created, replayed, err)
	}
	body, _ := json.Marshal(ProviderEvent{EventID: "provider-event-001", PaymentID: created.ID, ProviderReference: created.ProviderReference, Status: StatusCaptured, AmountMinor: 12300, Currency: "INR"})
	if _, _, err := service.HandleWebhook(MethodRazorpay, "bad-signature", body); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("invalid signature = %v", err)
	}
	captured, duplicate, err := service.HandleWebhook(MethodRazorpay, Sign(secret, body), body)
	if err != nil || duplicate || captured.Status != StatusCaptured {
		t.Fatalf("captured=%#v duplicate=%v err=%v", captured, duplicate, err)
	}
	replayedEvent, duplicate, err := service.HandleWebhook(MethodRazorpay, Sign(secret, body), body)
	if err != nil || !duplicate || replayedEvent.Status != StatusCaptured {
		t.Fatalf("event replay=%#v duplicate=%v err=%v", replayedEvent, duplicate, err)
	}
	reconciled, err := service.Reconcile(scope, created.ID, true)
	if err != nil || reconciled.Status != StatusReconciled {
		t.Fatalf("reconciled=%#v err=%v", reconciled, err)
	}
}

func TestPaymentRetryConflictRefundAndCountryPolicy(t *testing.T) {
	t.Parallel()
	secret := []byte("synthetic-provider-secret-32-bytes-minimum")
	service, _ := NewService(paymentClock, map[Method][]byte{MethodRazorpay: secret, MethodPaystack: secret})
	scope := paymentScope()
	first, _, _ := service.Create(scope, "idem-payment-create-0002", "order-payment-002", MethodRazorpay, Money{AmountMinor: 50000, Currency: "INR"})
	replay, replayed, err := service.Create(scope, "idem-payment-create-0002", "order-payment-002", MethodRazorpay, Money{AmountMinor: 50000, Currency: "INR"})
	if err != nil || !replayed || replay.ID != first.ID {
		t.Fatalf("replay=%#v replayed=%v err=%v", replay, replayed, err)
	}
	if _, _, err := service.Create(scope, "idem-payment-create-0002", "order-payment-002", MethodRazorpay, Money{AmountMinor: 1, Currency: "INR"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if _, _, err := service.Create(scope, "idem-payment-create-0003", "order-payment-003", MethodPaystack, Money{AmountMinor: 1, Currency: "INR"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("country provider policy = %v", err)
	}
	walletPayment, _, _ := service.Create(scope, "idem-payment-create-0004", "order-payment-004", MethodWallet, Money{AmountMinor: 100, Currency: "INR"})
	refunded, err := service.RequestRefund(scope, walletPayment.ID, Money{AmountMinor: 50, Currency: "INR"})
	if err != nil || refunded.Status != StatusRefundSubmitted {
		t.Fatalf("refund=%#v err=%v", refunded, err)
	}
}

func TestPaymentOwnershipAndPartialRefundWebhook(t *testing.T) {
	t.Parallel()
	secret := []byte("synthetic-provider-secret-32-bytes-minimum")
	service, _ := NewServiceWithProviders(paymentClock, map[Method][]byte{MethodRazorpay: secret}, map[Method]ProviderInitializer{MethodRazorpay: syntheticPaymentProvider{}})
	scope := paymentScope()
	created, _, _ := service.Create(scope, "idem-payment-owner-0001", "order-payment-owner-001", MethodRazorpay, Money{AmountMinor: 50000, Currency: "INR"})
	other := scope
	other.CustomerID = "customer-synthetic-002"
	if _, err := service.Get(other, created.ID); !errors.Is(err, ErrPaymentNotFound) {
		t.Fatalf("cross-customer get = %v", err)
	}
	captureBody, _ := json.Marshal(ProviderEvent{EventID: "provider-event-owner-001", PaymentID: created.ID, ProviderReference: created.ProviderReference, Status: StatusCaptured, AmountMinor: 50000, Currency: "INR"})
	_, _, _ = service.HandleWebhook(MethodRazorpay, Sign(secret, captureBody), captureBody)
	requested, err := service.RequestRefund(scope, created.ID, Money{AmountMinor: 12500, Currency: "INR"})
	if err != nil || requested.RefundAmount == nil || requested.RefundAmount.AmountMinor != 12500 {
		t.Fatalf("refund request = %#v err=%v", requested, err)
	}
	refundBody, _ := json.Marshal(ProviderEvent{EventID: "provider-event-owner-002", PaymentID: created.ID, ProviderReference: created.ProviderReference, Status: StatusRefunded, AmountMinor: 12500, Currency: "INR"})
	refunded, _, err := service.HandleWebhook(MethodRazorpay, Sign(secret, refundBody), refundBody)
	if err != nil || refunded.Status != StatusRefunded {
		t.Fatalf("refund webhook = %#v err=%v", refunded, err)
	}
}

type syntheticPaymentProvider struct{}

func (syntheticPaymentProvider) Initialize(_ context.Context, input ProviderInitialization) (ProviderSession, error) {
	return ProviderSession{ProviderReference: "order-synthetic-provider", ClientHandoff: ClientHandoff{Type: "RAZORPAY_CHECKOUT", PublicKey: "rzp_test_synthetic", ProviderOrderID: "order-synthetic-provider"}}, nil
}

func (syntheticPaymentProvider) Refund(_ context.Context, _ ProviderRefundRequest) (ProviderRefundSubmission, error) {
	return ProviderRefundSubmission{ProviderRefundReference: "refund-synthetic-provider", Status: StatusRefundSubmitted}, nil
}

func paymentScope() Scope {
	return Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
}
func paymentClock() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
