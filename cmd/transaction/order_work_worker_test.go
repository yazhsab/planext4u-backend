package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/supply"
)

type vendorWorkSourceFixture struct {
	intents []order.VendorWorkIntent
	acked   int
	retries []string
	ackWhen func() bool
}

func (fixture *vendorWorkSourceFixture) ClaimVendorWorkIntents(_ context.Context, limit int) ([]order.VendorWorkIntent, error) {
	if limit < 1 || limit > 100 {
		return nil, order.ErrInvalidRequest
	}
	return fixture.intents, nil
}

func (fixture *vendorWorkSourceFixture) AckVendorWorkIntent(_ context.Context, _, _ string) error {
	if fixture.ackWhen != nil && !fixture.ackWhen() {
		return errors.New("acknowledged before owner commit")
	}
	fixture.acked++
	return nil
}

func (fixture *vendorWorkSourceFixture) RetryVendorWorkIntent(_ context.Context, _, _, code string) error {
	fixture.retries = append(fixture.retries, code)
	return nil
}

type vendorWorkOrderFixture struct{ value order.Order }

func (fixture vendorWorkOrderFixture) Get(_ order.Scope, _ string) (order.Order, error) {
	return fixture.value, nil
}

type vendorWorkOwnerFixture struct {
	calls     int
	committed bool
	err       error
}

type vendorWorkCancellationSourceFixture struct {
	intents []order.VendorWorkCancellationIntent
	acked   int
	retries []string
	ackWhen func() bool
}

func (fixture *vendorWorkCancellationSourceFixture) ClaimVendorWorkCancellations(_ context.Context, limit int) ([]order.VendorWorkCancellationIntent, error) {
	if limit < 1 || limit > 100 {
		return nil, order.ErrInvalidRequest
	}
	return fixture.intents, nil
}

func (fixture *vendorWorkCancellationSourceFixture) AckVendorWorkCancellation(_ context.Context, _, _ string) error {
	if fixture.ackWhen != nil && !fixture.ackWhen() {
		return errors.New("acknowledged before owner commit")
	}
	fixture.acked++
	return nil
}

func (fixture *vendorWorkCancellationSourceFixture) RetryVendorWorkCancellation(_ context.Context, _, _, code string) error {
	fixture.retries = append(fixture.retries, code)
	return nil
}

type vendorWorkCancellationOwnerFixture struct {
	calls     int
	committed bool
	request   supply.OrderWorkCancellation
	outcome   supply.CancellationOutcome
	err       error
}

func (fixture *vendorWorkCancellationOwnerFixture) IngestOrderWorkCancellation(_ context.Context, request supply.OrderWorkCancellation) (supply.CancellationOutcome, error) {
	fixture.calls++
	fixture.request = request
	if fixture.err != nil {
		return supply.CancellationOutcome{}, fixture.err
	}
	fixture.committed = true
	return fixture.outcome, nil
}

func TestVendorWorkCancellationDeliveryAcksOnlyAfterOwnerCommit(t *testing.T) {
	intent := vendorWorkCancellationFixture()
	source := &vendorWorkCancellationSourceFixture{intents: []order.VendorWorkCancellationIntent{intent}}
	owner := &vendorWorkCancellationOwnerFixture{outcome: supply.CancellationOutcome{Outcome: "CANCELLED"}}
	source.ackWhen = func() bool { return owner.committed }
	delivered, discrepancies, err := deliverVendorWorkCancellations(context.Background(), source, owner, 1)
	if err != nil || delivered != 1 || discrepancies != 0 || source.acked != 1 || owner.calls != 1 || len(source.retries) != 0 {
		t.Fatalf("cancellation delivery=%d discrepancies=%d err=%v ack=%d owner=%d retry=%#v", delivered, discrepancies, err, source.acked, owner.calls, source.retries)
	}
	if owner.request.ID != intent.ID || owner.request.SourceWorkIntentID != intent.SourceWorkIntentID ||
		owner.request.TenantID != intent.TenantID || owner.request.Country != intent.Country ||
		owner.request.CustomerID != intent.CustomerID || owner.request.OrderID != intent.OrderID ||
		owner.request.VendorID != intent.VendorID || owner.request.OrderRevision != intent.OrderRevision ||
		owner.request.OrderStatus != intent.OrderStatus {
		t.Fatalf("cancellation source changed: %#v", owner.request)
	}
}

func TestVendorWorkCancellationDeliveryRetriesFailureAndUnexpectedOutcome(t *testing.T) {
	intent := vendorWorkCancellationFixture()
	source := &vendorWorkCancellationSourceFixture{intents: []order.VendorWorkCancellationIntent{intent}}
	owner := &vendorWorkCancellationOwnerFixture{err: supply.ErrIdempotencyConflict}
	if delivered, discrepancies, err := deliverVendorWorkCancellations(context.Background(), source, owner, 1); err != nil || delivered != 0 || discrepancies != 0 || source.acked != 0 || len(source.retries) != 1 || source.retries[0] != "SOURCE_CONFLICT" {
		t.Fatalf("conflicting cancellation delivery=%d discrepancies=%d err=%v ack=%d retry=%#v", delivered, discrepancies, err, source.acked, source.retries)
	}
	owner.err = nil
	owner.outcome.Outcome = "UNKNOWN"
	if delivered, _, err := deliverVendorWorkCancellations(context.Background(), source, owner, 1); err != nil || delivered != 0 || source.acked != 0 || len(source.retries) != 2 || source.retries[1] != "UNEXPECTED_OUTCOME" {
		t.Fatalf("unexpected cancellation delivery=%d err=%v ack=%d retry=%#v", delivered, err, source.acked, source.retries)
	}
	owner.outcome.Outcome = "ALREADY_COMPLETED"
	if delivered, discrepancies, err := deliverVendorWorkCancellations(context.Background(), source, owner, 1); err != nil || delivered != 1 || discrepancies != 1 || source.acked != 1 {
		t.Fatalf("completed work cancellation delivery=%d discrepancies=%d err=%v ack=%d", delivered, discrepancies, err, source.acked)
	}
}

func vendorWorkCancellationFixture() order.VendorWorkCancellationIntent {
	const (
		orderID  = "30000000-0000-4000-8000-000000000101"
		vendorID = "60000000-0000-4000-8000-000000000101"
	)
	return order.VendorWorkCancellationIntent{
		ID:                 uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-cancel:"+orderID+":"+vendorID+":2:CANCEL_REQUESTED")).String(),
		SourceWorkIntentID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+orderID+":"+vendorID)).String(),
		TenantID:           "559d37a4-2458-43f8-886d-89d33aa8e482", Country: "IN",
		CustomerID: "78b2e2e4-bc18-45af-89f9-b139aceab1e4", OrderID: orderID,
		VendorID: vendorID, OrderRevision: 2, OrderStatus: order.StatusCancelRequested,
		ClaimToken: uuid.NewString(), ClaimUntil: time.Now().Add(time.Minute),
	}
}

func (fixture *vendorWorkOwnerFixture) IngestOrderWork(_ context.Context, _ supply.OrderWorkIngress) (supply.WorkItem, bool, error) {
	fixture.calls++
	if fixture.err != nil {
		return supply.WorkItem{}, false, fixture.err
	}
	fixture.committed = true
	return supply.WorkItem{ID: uuid.NewString()}, true, nil
}

func TestVendorWorkDeliveryClosesCancelledPlacementAndAcknowledgesOnlyAfterOwnerCommit(t *testing.T) {
	intent, current := vendorWorkDeliveryFixture()
	source := &vendorWorkSourceFixture{intents: []order.VendorWorkIntent{intent}}
	owner := &vendorWorkOwnerFixture{}
	current.Status = order.StatusCancelled
	if delivered, err := deliverVendorWorkIntents(context.Background(), source, vendorWorkOrderFixture{current}, owner, 1); err != nil || delivered != 0 || owner.calls != 0 || source.acked != 1 || len(source.retries) != 0 {
		t.Fatalf("pre-cancelled delivery=%d err=%v owner=%d ack=%d retry=%#v", delivered, err, owner.calls, source.acked, source.retries)
	}
	current.Status = order.StatusPlaced
	source.acked = 0
	source.ackWhen = func() bool { return owner.committed }
	if delivered, err := deliverVendorWorkIntents(context.Background(), source, vendorWorkOrderFixture{current}, owner, 1); err != nil || delivered != 1 || owner.calls != 1 || source.acked != 1 || len(source.retries) != 0 {
		t.Fatalf("owner delivery=%d err=%v owner=%d ack=%d retry=%#v", delivered, err, owner.calls, source.acked, source.retries)
	}
}

func TestVendorWorkDeliveryClosesPlacementAfterSupplyTombstone(t *testing.T) {
	intent, current := vendorWorkDeliveryFixture()
	source := &vendorWorkSourceFixture{intents: []order.VendorWorkIntent{intent}}
	owner := &vendorWorkOwnerFixture{err: supply.ErrOrderWorkCancelled}
	if delivered, err := deliverVendorWorkIntents(context.Background(), source, vendorWorkOrderFixture{current}, owner, 1); err != nil || delivered != 0 || owner.calls != 1 || source.acked != 1 || len(source.retries) != 0 {
		t.Fatalf("tombstoned placement=%d err=%v owner=%d ack=%d retry=%#v", delivered, err, owner.calls, source.acked, source.retries)
	}
}

func TestVendorWorkDeliveryRejectsChangedSourceAndRetriesInactiveVendor(t *testing.T) {
	intent, current := vendorWorkDeliveryFixture()
	source := &vendorWorkSourceFixture{intents: []order.VendorWorkIntent{intent}}
	owner := &vendorWorkOwnerFixture{}
	current.Snapshot.Lines[0].ItemName = "changed after placement"
	if delivered, err := deliverVendorWorkIntents(context.Background(), source, vendorWorkOrderFixture{current}, owner, 1); err != nil || delivered != 0 || owner.calls != 0 || source.retries[0] != "SOURCE_MISMATCH" {
		t.Fatalf("changed source delivery=%d err=%v owner=%d retry=%#v", delivered, err, owner.calls, source.retries)
	}
	current.Snapshot.Lines[0].ItemName = intent.Lines[0].ItemName
	owner.err = supply.ErrOrderWorkVendorInactive
	if delivered, err := deliverVendorWorkIntents(context.Background(), source, vendorWorkOrderFixture{current}, owner, 1); err != nil || delivered != 0 || source.acked != 0 || source.retries[1] != "VENDOR_NOT_ACTIVE" {
		t.Fatalf("inactive vendor delivery=%d err=%v ack=%d retry=%#v", delivered, err, source.acked, source.retries)
	}
}

func TestVendorWorkDeliveryRemainsExplicitlyLocalOptIn(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "DATABASE_URL": "postgres://transaction:secret@database.example/p4u?sslmode=verify-full",
		"TENANT_ID": "afc1e0db-73cf-40b3-9927-33590133da0b", "SUPPORTED_COUNTRIES": "IN",
		"WALLET_PROGRAM_FILE": "/run/config/wallet-program.json", "BOOKING_OTP_KEY_FILE": "/run/secrets/booking-otp-key",
		"EMERGENCY_DATA_KEY_FILE": "/run/secrets/emergency-data-key", "LOCAL_VERTICALS_CONTACT_KEY_FILE": "/run/secrets/local-contact-key",
		"RAZORPAY_KEY_ID": "rzp_live_publickey0001", "RAZORPAY_API_SECRET_FILE": "/run/secrets/razorpay-api",
		"RAZORPAY_WEBHOOK_SECRET_FILE": "/run/secrets/razorpay-webhook", "NOTIFICATION_BASE_URL": "https://notification.internal.example",
		"ORDER_NOTIFICATION_HMAC_KEY_FILE": "/run/secrets/order-notification",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	defaulted, err := loadRuntimeConfig(lookup)
	if err != nil || defaulted.vendorWorkDeliveryEnabled {
		t.Fatalf("default vendor work delivery=%t err=%v", defaulted.vendorWorkDeliveryEnabled, err)
	}
	values["ORDER_VENDOR_WORK_DELIVERY_ENABLED"] = "true"
	if _, err := loadRuntimeConfig(lookup); err == nil {
		t.Fatal("production vendor work delivery enabled before cancellation propagation")
	}
	values["APP_ENV"] = "development"
	local, err := loadRuntimeConfig(lookup)
	if err != nil || !local.vendorWorkDeliveryEnabled {
		t.Fatalf("explicit local vendor work delivery=%t err=%v", local.vendorWorkDeliveryEnabled, err)
	}
}

func vendorWorkDeliveryFixture() (order.VendorWorkIntent, order.Order) {
	const (
		orderID  = "30000000-0000-4000-8000-000000000101"
		vendorID = "60000000-0000-4000-8000-000000000101"
	)
	line := order.LineSnapshot{
		ItemID: "40000000-0000-4000-8000-000000000101", VariantID: "50000000-0000-4000-8000-000000000101",
		VendorID: vendorID, ItemName: "Local oil", Quantity: 1,
		UnitPrice: order.Money{AmountMinor: 10000, Currency: "INR"},
		LineTotal: order.Money{AmountMinor: 10000, Currency: "INR"},
	}
	intent := order.VendorWorkIntent{
		ID:       uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+orderID+":"+vendorID)).String(),
		TenantID: "559d37a4-2458-43f8-886d-89d33aa8e482", Country: "IN",
		CustomerID: "78b2e2e4-bc18-45af-89f9-b139aceab1e4", OrderID: orderID,
		VendorID: vendorID, OrderRevision: 1, Lines: []order.LineSnapshot{line},
		Currency: "INR", ProductTaxTreatment: "EXCLUSIVE", ClaimToken: uuid.NewString(),
		ClaimUntil: time.Now().Add(time.Minute),
	}
	current := order.Order{
		ID: orderID, Revision: 2, Status: order.StatusPlaced,
		Snapshot: order.CheckoutSnapshot{
			Lines: []order.LineSnapshot{line}, Total: order.Money{AmountMinor: 10000, Currency: "INR"},
			ProductTaxTreatment: "EXCLUSIVE",
		},
	}
	return intent, current
}
