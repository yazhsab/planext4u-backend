package order

import (
	"errors"
	"testing"
	"time"
)

func TestOrderStateMachinePODAndRating(t *testing.T) {
	t.Parallel()
	service, _ := NewService(orderClock)
	scope := orderScope()
	value, _, err := service.Create(scope, "idem-order-create-0001", orderSnapshot(), true)
	if err != nil || value.Status != StatusPlaced {
		t.Fatalf("create=%#v err=%v", value, err)
	}
	if _, _, err := service.Transition(scope, "idem-order-invalid-0001", value.ID, value.Revision, StatusPickedUp, "RIDER", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition = %v", err)
	}
	steps := []struct {
		status Status
		actor  string
	}{{StatusAccepted, "VENDOR"}, {StatusPacking, "VENDOR"}, {StatusReadyForHandover, "VENDOR"}, {StatusAssigned, "PLATFORM"}, {StatusPickedUp, "RIDER"}, {StatusOutForDelivery, "RIDER"}}
	for index, step := range steps {
		value, _, err = service.Transition(scope, "idem-order-step-000"+string(rune('1'+index)), value.ID, value.Revision, step.status, step.actor, "")
		if err != nil {
			t.Fatalf("step %s: %v", step.status, err)
		}
	}
	if _, _, err := service.RecordPOD(scope, "idem-order-pod-invalid-0001", value.ID, value.Revision, Proof{PolicyVersion: "pod-v1"}); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("missing POD = %v", err)
	}
	value, _, err = service.RecordPOD(scope, "idem-order-pod-0001", value.ID, value.Revision, Proof{PolicyVersion: "pod-v1", OTPVerified: true})
	if err != nil || value.Status != StatusDeliveredPendingConfirmation {
		t.Fatalf("pod=%#v err=%v", value, err)
	}
	value, _, _ = service.Transition(scope, "idem-order-confirm-0001", value.ID, value.Revision, StatusCompleted, "CUSTOMER", "")
	value, _, err = service.Rate(scope, "idem-order-rating-0001", value.ID, value.Revision, 5, "Excellent local delivery")
	if err != nil || value.Rating == nil || value.Rating.Score != 5 {
		t.Fatalf("rating=%#v err=%v", value.Rating, err)
	}
}

func TestPartialReturnRefundAndRevisionReplay(t *testing.T) {
	t.Parallel()
	service, _ := NewService(orderClock)
	scope := orderScope()
	value, _, _ := service.Create(scope, "idem-order-create-0002", orderSnapshot(), true)
	steps := []struct {
		status Status
		actor  string
	}{{StatusAccepted, "VENDOR"}, {StatusPacking, "VENDOR"}, {StatusReadyForHandover, "VENDOR"}, {StatusAssigned, "PLATFORM"}, {StatusPickedUp, "RIDER"}, {StatusOutForDelivery, "RIDER"}}
	for index, step := range steps {
		value, _, _ = service.Transition(scope, "idem-return-step-000"+string(rune('1'+index)), value.ID, value.Revision, step.status, step.actor, "")
	}
	value, _, _ = service.RecordPOD(scope, "idem-return-pod-0001", value.ID, value.Revision, Proof{PolicyVersion: "pod-v1", PhotoAssetID: "asset-pod-001"})
	value, _, _ = service.Transition(scope, "idem-return-confirm-0001", value.ID, value.Revision, StatusCompleted, "CUSTOMER", "")
	requested, _, err := service.RequestReturn(scope, "idem-return-request-0001", value.ID, value.Revision, []ReturnLine{{VariantID: "variant-oil", Quantity: 1}}, "One bottle was damaged")
	if err != nil || requested.Return == nil || requested.Return.RefundAmount.AmountMinor != 9500 {
		t.Fatalf("return=%#v err=%v", requested.Return, err)
	}
	approved, _, _ := service.DecideReturn(scope, "idem-return-approve-0001", value.ID, requested.Revision, true, "Evidence accepted")
	returned, _, _ := service.Transition(scope, "idem-return-received-0001", value.ID, approved.Revision, StatusReturned, "PLATFORM", "")
	refunded, _, err := service.RecordRefund(scope, "idem-return-refund-0001", value.ID, returned.Revision, "refund-provider-001", returned.Return.RefundAmount)
	if err != nil || refunded.Status != StatusRefunded {
		t.Fatalf("refund=%#v err=%v", refunded, err)
	}
	replay, replayed, err := service.RecordRefund(scope, "idem-return-refund-0001", value.ID, returned.Revision, "refund-provider-001", returned.Return.RefundAmount)
	if err != nil || !replayed || replay.Revision != refunded.Revision {
		t.Fatalf("refund replay=%#v replayed=%v err=%v", replay, replayed, err)
	}
}

func TestOrderOwnershipIsolation(t *testing.T) {
	t.Parallel()
	service, _ := NewService(orderClock)
	scope := orderScope()
	created, _, _ := service.Create(scope, "idem-order-owner-0001", orderSnapshot(), true)
	other := scope
	other.CustomerID = "customer-synthetic-002"
	if _, err := service.Get(other, created.ID); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("cross-customer get = %v", err)
	}
	listed, err := service.List(other)
	if err != nil || len(listed) != 0 {
		t.Fatalf("cross-customer list = %#v err=%v", listed, err)
	}
	if _, _, err := service.Transition(other, "idem-order-owner-0002", created.ID, created.Revision, StatusAccepted, "VENDOR", ""); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("cross-customer mutation = %v", err)
	}
}

func orderSnapshot() CheckoutSnapshot {
	return CheckoutSnapshot{
		CartRevision: 3, Lines: []LineSnapshot{{VariantID: "variant-oil", ItemID: "item-oil", ItemName: "Local oil", VariantName: "500ml", Quantity: 2, UnitPrice: Money{AmountMinor: 10000, Currency: "INR"}, LineTotal: Money{AmountMinor: 20000, Currency: "INR"}, VendorID: "vendor-local-001", TaxMinor: 800, DiscountMinor: 1800}},
		Address: AddressSnapshot{AddressID: "address-001", Label: "Home", PostalCode: "641001", Locality: "Coimbatore"}, Delivery: DeliverySnapshot{SlotID: "tomorrow-standard", WindowStart: orderClock().Add(24 * time.Hour), WindowEnd: orderClock().Add(28 * time.Hour), Fee: Money{AmountMinor: 1000, Currency: "INR"}},
		Subtotal: Money{AmountMinor: 20000, Currency: "INR"}, Discount: Money{AmountMinor: 1800, Currency: "INR"}, Tax: Money{AmountMinor: 800, Currency: "INR"}, Fees: Money{AmountMinor: 1000, Currency: "INR"}, WalletApplied: Money{AmountMinor: 100, Currency: "INR"}, Total: Money{AmountMinor: 19900, Currency: "INR"},
		PromotionCode: "LOCAL10", PricingPolicyVersion: "pricing-2026-01", ReservationID: "reservation-001", PaymentID: "payment-001", PaymentMethod: "RAZORPAY",
	}
}
func orderScope() Scope {
	return Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
}
func orderClock() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
