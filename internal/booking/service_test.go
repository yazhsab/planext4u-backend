package booking

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

func TestBEP4002ConcurrentLastSlotHoldHasOneWinner(t *testing.T) {
	service, _, _, _ := bookingFixture(t)
	var successes atomic.Int64
	var wait sync.WaitGroup
	for index := 0; index < 24; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := service.Hold(testScope(), fmt.Sprintf("hold-concurrent-%04d", index), "slot-cleaning-001", "600001")
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, ErrSlotUnavailable) {
				t.Errorf("hold %d error = %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful holds = %d, want 1", got)
	}
}

func TestBEP4002ExpiredHoldReleasesCapacityAndIdempotencyIsBound(t *testing.T) {
	service, _, now, _ := bookingFixture(t)
	hold, replay, err := service.Hold(testScope(), "hold-expiry-command-0001", "slot-cleaning-001", "600001")
	if err != nil || replay || hold.Status != HoldActive {
		t.Fatalf("hold = %#v replay=%v err=%v", hold, replay, err)
	}
	if _, _, err := service.Hold(testScope(), "hold-expiry-command-0001", "slot-cleaning-002", "600001"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	*now = now.Add(6 * time.Minute)
	slots, err := service.Slots(testScope(), "service-home-cleaning", now.Add(-time.Minute), now.Add(72*time.Hour))
	if err != nil || slots[0].Remaining != 1 {
		t.Fatalf("expired capacity slots=%#v err=%v", slots, err)
	}
	if _, _, err := service.Create(context.Background(), testScope(), "booking-expired-hold-0001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet}); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("expired hold create error = %v", err)
	}
}

func TestBEP4003WalletBookingLifecycleRequiresOTPAndCompletionEvidence(t *testing.T) {
	service, walletService, _, _ := bookingFixture(t)
	hold, _, err := service.Hold(testScope(), "hold-lifecycle-command-01", "slot-cleaning-002", "600001")
	if err != nil {
		t.Fatal(err)
	}
	created, replay, err := service.Create(context.Background(), testScope(), "booking-lifecycle-00001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	if err != nil || replay || created.Status != StatusRequested || created.Payment.Status != payment.StatusCaptured || created.Revision != 1 {
		t.Fatalf("created = %#v replay=%v err=%v", created, replay, err)
	}
	again, replay, err := service.Create(context.Background(), testScope(), "booking-lifecycle-00001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	if err != nil || !replay || again.ID != created.ID {
		t.Fatalf("create replay = %#v replay=%v err=%v", again, replay, err)
	}
	account, _ := walletService.Account(wallet.Scope{TenantID: testScope().TenantID, Country: "IN", CustomerID: testScope().CustomerID})
	if account.Balance != 95000 {
		t.Fatalf("wallet balance = %d, want 95000", account.Balance)
	}

	provider := testProviderActor()
	value, _, err := service.ProviderTransition(provider, "provider-accept-command-01", created.ID, 1, StatusAccepted, "")
	if err != nil || value.StartOTP == "" || value.Revision != 2 {
		t.Fatalf("accepted = %#v err=%v", value, err)
	}
	value, _, _ = service.ProviderTransition(provider, "provider-enroute-command-1", value.ID, value.Revision, StatusProviderEnRoute, "")
	value, _, _ = service.ProviderTransition(provider, "provider-arrived-command-1", value.ID, value.Revision, StatusArrived, "")
	value, _, err = service.ProviderTransition(provider, "provider-otp-required-01", value.ID, value.Revision, StatusStartOTPRequired, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Start(provider, "provider-wrong-otp-0001", value.ID, value.Revision, "000000"); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("wrong OTP error = %v", err)
	}
	value, _, err = service.Start(provider, "provider-correct-otp-01", value.ID, value.Revision, value.StartOTP)
	if err != nil || value.Status != StatusInProgress || value.StartOTP != "" {
		t.Fatalf("started = %#v err=%v", value, err)
	}
	if _, _, err := service.Complete(provider, "provider-early-proof-001", value.ID, value.Revision, "media-proof-001"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("early completion error = %v", err)
	}
	value, _, err = service.ProviderTransition(provider, "provider-proof-required-1", value.ID, value.Revision, StatusCompletionEvidenceRequired, "")
	if err != nil {
		t.Fatal(err)
	}
	value, _, err = service.Complete(provider, "provider-proof-submit-001", value.ID, value.Revision, "media-proof-001")
	if err != nil || value.Status != StatusCompletedPendingConfirmation || value.CompletionEvidence == nil {
		t.Fatalf("completion = %#v err=%v", value, err)
	}
	value, _, err = service.ConfirmCompletion(testScope(), "customer-confirm-complete-1", value.ID, value.Revision)
	if err != nil || value.Status != StatusCompleted || value.Revision != 9 {
		t.Fatalf("confirmed = %#v err=%v", value, err)
	}
}

func TestBEP4003RescheduleAndCancellationRestoreWalletAndCapacity(t *testing.T) {
	service, walletService, now, _ := bookingFixture(t)
	hold, _, _ := service.Hold(testScope(), "hold-reschedule-source-01", "slot-cleaning-001", "600001")
	created, _, err := service.Create(context.Background(), testScope(), "booking-reschedule-0001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	if err != nil {
		t.Fatal(err)
	}
	newHold, _, err := service.Hold(testScope(), "hold-reschedule-target-01", "slot-cleaning-002", "600001")
	if err != nil {
		t.Fatal(err)
	}
	value, replay, err := service.Reschedule(testScope(), "booking-reschedule-command-1", created.ID, created.Revision, RescheduleRequest{HoldID: newHold.ID, Reason: "Customer schedule changed"})
	if err != nil || replay || value.Slot.ID != "slot-cleaning-002" || value.RescheduleCount != 1 || value.FreeReschedulesLeft != 0 || value.Revision != 3 {
		t.Fatalf("rescheduled = %#v replay=%v err=%v", value, replay, err)
	}
	thirdHold, _, err := service.Hold(testScope(), "hold-reschedule-third-001", "slot-cleaning-003", "600001")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Reschedule(testScope(), "booking-reschedule-command-2", value.ID, value.Revision, RescheduleRequest{HoldID: thirdHold.ID, Reason: "Another schedule change"}); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("second free reschedule error = %v", err)
	}
	value, _, err = service.Cancel(testScope(), "booking-cancel-command-001", value.ID, value.Revision, "Plans changed")
	if err != nil || value.Status != StatusCancelled || value.Payment.Status != payment.StatusRefundSubmitted {
		t.Fatalf("cancelled = %#v err=%v", value, err)
	}
	account, _ := walletService.Account(wallet.Scope{TenantID: testScope().TenantID, Country: "IN", CustomerID: testScope().CustomerID})
	if account.Balance != 100000 {
		t.Fatalf("restored wallet balance = %d, want 100000", account.Balance)
	}
	slots, _ := service.Slots(testScope(), "service-home-cleaning", now.Add(-time.Minute), now.Add(96*time.Hour))
	remaining := map[string]int{}
	for _, slot := range slots {
		remaining[slot.ID] = slot.Remaining
	}
	if remaining["slot-cleaning-001"] != 1 || remaining["slot-cleaning-002"] != 2 {
		t.Fatalf("released capacity = %#v", remaining)
	}
}

func TestBEP4003PendingProviderPaymentExpiresWithoutStrandingCapacity(t *testing.T) {
	service, _, now, _ := bookingFixture(t)
	hold, _, _ := service.Hold(testScope(), "hold-provider-payment-001", "slot-cleaning-001", "600001")
	value, _, err := service.Create(context.Background(), testScope(), "booking-provider-pay-0001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodRazorpay})
	if err != nil || value.Status != StatusPendingPayment {
		t.Fatalf("pending booking = %#v err=%v", value, err)
	}
	*now = now.Add(6 * time.Minute)
	actor := Actor{TenantID: testScope().TenantID, Country: "IN", Subject: testScope().CustomerID, Roles: []string{"CUSTOMER"}}
	value, err = service.Get(actor, value.ID)
	if err != nil || value.Status != StatusCancelled || value.Payment.Status != payment.StatusCancelled {
		t.Fatalf("expired booking = %#v err=%v", value, err)
	}
	slots, _ := service.Slots(testScope(), "service-home-cleaning", now.Add(-time.Minute), now.Add(72*time.Hour))
	if slots[0].Remaining != 1 {
		t.Fatalf("remaining after payment expiry = %d", slots[0].Remaining)
	}
}

func TestBEP4004NoShowCanBeDisputedByOwningCustomerOnly(t *testing.T) {
	service, _, _, _ := bookingFixture(t)
	hold, _, _ := service.Hold(testScope(), "hold-no-show-command-001", "slot-cleaning-002", "600001")
	value, _, _ := service.Create(context.Background(), testScope(), "booking-no-show-command1", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	value, _, _ = service.ProviderTransition(testProviderActor(), "provider-accept-no-show-1", value.ID, value.Revision, StatusAccepted, "")
	customer := Actor{TenantID: testScope().TenantID, Country: "IN", Subject: testScope().CustomerID, Roles: []string{"CUSTOMER"}}
	value, _, err := service.NoShow(customer, "customer-provider-no-show1", value.ID, value.Revision, "Provider did not arrive")
	if err != nil || value.Status != StatusProviderNoShow {
		t.Fatalf("no-show = %#v err=%v", value, err)
	}
	attacker := Actor{TenantID: testScope().TenantID, Country: "IN", Subject: "customer-attacker", Roles: []string{"CUSTOMER"}}
	if _, _, err := service.Dispute(attacker, "attacker-dispute-command1", value.ID, value.Revision, "Unauthorized dispute"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("attacker dispute error = %v", err)
	}
	value, _, err = service.Dispute(customer, "customer-dispute-command-1", value.ID, value.Revision, "Provider no-show fee disputed")
	if err != nil || value.Status != StatusDisputed {
		t.Fatalf("dispute = %#v err=%v", value, err)
	}
}

func bookingFixture(t *testing.T) (*Service, *wallet.Service, *time.Time, *payment.Service) {
	t.Helper()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	paymentService, err := payment.NewService(clock, map[payment.Method][]byte{
		payment.MethodRazorpay: []byte("razorpay-synthetic-secret-at-least-32-bytes"),
		payment.MethodPaystack: []byte("paystack-synthetic-secret-at-least-32-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	walletService, err := wallet.NewService(clock, wallet.RewardPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := walletService.Credit(wallet.Scope{TenantID: testScope().TenantID, Country: "IN", CustomerID: testScope().CustomerID}, "booking-wallet-seed-0001", "TEST_SEED", "booking-wallet-seed", 100000, now.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	offering := Offering{
		ID: "service-home-cleaning", ProviderID: "provider-clean-001", ProviderName: "Clean Chennai",
		CategoryID: "home-cleaning", Name: "Home deep cleaning", Summary: "Verified two-person cleaning team",
		DurationMinutes: 120, Price: Money{AmountMinor: 20000, Currency: "INR"}, Advance: Money{AmountMinor: 5000, Currency: "INR"}, PaymentMode: PaymentAdvance,
		VerifiedProvider: true, RatingAverage: 4.8, CompletedBookings: 241, LiveEngagements: 3,
		ServicePostalCodes: []string{"600001", "600002"}, CancellationPolicyRef: "service-cancel-v1", ReschedulePolicyRef: "service-reschedule-v1", Active: true,
	}
	configuration := Configuration{
		Offerings: []Offering{offering},
		Slots: []Slot{
			{ID: "slot-cleaning-001", OfferingID: offering.ID, ProviderID: offering.ProviderID, StartsAt: now.Add(24 * time.Hour), EndsAt: now.Add(26 * time.Hour), TimeZone: "Asia/Kolkata", Capacity: 1, BufferMinutes: 30, Price: offering.Price, Advance: offering.Advance, PolicyVersion: "slot-policy-v1", ServiceDate: "2026-08-29", ProviderVersion: 1},
			{ID: "slot-cleaning-002", OfferingID: offering.ID, ProviderID: offering.ProviderID, StartsAt: now.Add(48 * time.Hour), EndsAt: now.Add(50 * time.Hour), TimeZone: "Asia/Kolkata", Capacity: 2, BufferMinutes: 30, Price: offering.Price, Advance: offering.Advance, PolicyVersion: "slot-policy-v1", ServiceDate: "2026-08-30", ProviderVersion: 1},
			{ID: "slot-cleaning-003", OfferingID: offering.ID, ProviderID: offering.ProviderID, StartsAt: now.Add(72 * time.Hour), EndsAt: now.Add(74 * time.Hour), TimeZone: "Asia/Kolkata", Capacity: 2, BufferMinutes: 30, Price: offering.Price, Advance: offering.Advance, PolicyVersion: "slot-policy-v1", ServiceDate: "2026-08-31", ProviderVersion: 1},
		},
		Policies: []Policy{{Version: "service-policy-v1", Country: "IN", HoldTTL: 5 * time.Minute, CancellationCutoff: 2 * time.Hour, MaximumFreeReschedules: 1, StartOTPValidity: 30 * time.Minute, CompletionConfirmWindow: 24 * time.Hour, WalletPointValueMinor: 1}},
	}
	service, err := NewService(paymentService, walletService, configuration, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service, walletService, &now, paymentService
}

func testScope() Scope {
	return Scope{TenantID: "tenant-synthetic-001", Country: "IN", CustomerID: "customer-synthetic-001"}
}

func testProviderActor() Actor {
	return Actor{TenantID: testScope().TenantID, Country: "IN", Subject: "provider-clean-001", Roles: []string{"SERVICE_VENDOR"}}
}
