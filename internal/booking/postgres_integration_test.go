//go:build integration

package booking

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

func TestPostgresBookingCapacityReplayRestartLifecycleAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("BOOKING_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("BOOKING_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS booking CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS booking CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000004_booking_roles.up.sql",
		"../../migrations/booking/000001_booking.up.sql",
		"../../migrations/booking/000002_durable_runtime.up.sql",
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
	seedPostgresBooking(t, ctx, pool, now)
	payments := newBookingPaymentFake(now)
	wallets := &bookingWalletFake{}
	otpKey := []byte("booking-otp-test-key-32-bytes!!!")
	service, err := NewPostgresService(pool, payments, wallets, func() time.Time { return now }, otpKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: bookingTenantOne, Country: "IN", CustomerID: bookingCustomerOne}
	offerings, err := service.Offerings(scope, "600001", "")
	if err != nil || len(offerings) != 1 || offerings[0].ProviderName != "P4U Verified Services" {
		t.Fatalf("offerings=%#v err=%v", offerings, err)
	}
	slots, err := service.Slots(scope, bookingOfferingOne, now, now.Add(48*time.Hour))
	if err != nil || len(slots) != 2 || slots[0].Remaining != 1 {
		t.Fatalf("slots=%#v err=%v", slots, err)
	}

	otherScope := Scope{TenantID: bookingTenantOne, Country: "IN", CustomerID: bookingCustomerTwo}
	type holdResult struct {
		value SlotHold
		err   error
	}
	holds := make(chan holdResult, 2)
	var wait sync.WaitGroup
	for index, candidate := range []Scope{scope, otherScope} {
		wait.Add(1)
		go func(index int, candidate Scope) {
			defer wait.Done()
			value, _, callErr := service.Hold(candidate, "booking-hold-concurrent-000"+string(rune('1'+index)), bookingSlotOne, "600001")
			holds <- holdResult{value: value, err: callErr}
		}(index, candidate)
	}
	wait.Wait()
	close(holds)
	var winningScope Scope
	var hold SlotHold
	succeeded, unavailable := 0, 0
	for result := range holds {
		if result.err == nil {
			succeeded++
			hold = result.value
			if result.value.scope.CustomerID == bookingCustomerOne {
				winningScope = scope
			} else {
				winningScope = otherScope
			}
		} else if errors.Is(result.err, ErrSlotUnavailable) {
			unavailable++
		} else {
			t.Fatalf("unexpected hold error: %v", result.err)
		}
	}
	if succeeded != 1 || unavailable != 1 {
		t.Fatalf("capacity race succeeded=%d unavailable=%d", succeeded, unavailable)
	}
	created, replay, err := service.Create(ctx, winningScope, "booking-create-postgres-0001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	if err != nil || replay || created.Status != StatusRequested || created.Revision != 1 || created.Payment.Status != payment.StatusCaptured {
		t.Fatalf("created=%#v replay=%t err=%v", created, replay, err)
	}
	restarted, err := NewPostgresService(pool, payments, wallets, func() time.Time { return now }, otpKey)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := restarted.Create(ctx, winningScope, "booking-create-postgres-0001", CreateBookingRequest{HoldID: hold.ID, PaymentMethod: payment.MethodWallet})
	if err != nil || !replay || replayed.ID != created.ID || payments.CreateCalls() != 1 || wallets.RedeemCalls() != 1 {
		t.Fatalf("restart replay=%#v replay=%t payments=%d wallets=%d err=%v", replayed, replay, payments.CreateCalls(), wallets.RedeemCalls(), err)
	}
	provider := Actor{TenantID: bookingTenantOne, Country: "IN", Subject: bookingProviderOne, Roles: []string{"SERVICE_VENDOR"}}
	accepted, _, err := restarted.ProviderTransition(provider, "booking-accept-postgres-001", created.ID, 1, StatusAccepted, "Accepted by provider")
	if err != nil || accepted.StartOTP == "" || accepted.Revision != 2 {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	loaded, err := restarted.Get(Actor{TenantID: winningScope.TenantID, Country: winningScope.Country, Subject: winningScope.CustomerID, Roles: []string{"CUSTOMER"}}, created.ID)
	if err != nil || loaded.StartOTP != accepted.StartOTP || len(loaded.Timeline) != 2 {
		t.Fatalf("customer loaded=%#v err=%v", loaded, err)
	}
	for _, transition := range []Status{StatusProviderEnRoute, StatusArrived, StatusStartOTPRequired} {
		accepted, _, err = restarted.ProviderTransition(provider, "booking-transition-"+stringsForKey(string(transition)), created.ID, accepted.Revision, transition, "Provider progress")
		if err != nil {
			t.Fatalf("transition %s: %v", transition, err)
		}
	}
	started, _, err := restarted.Start(provider, "booking-start-postgres-0001", created.ID, accepted.Revision, loaded.StartOTP)
	if err != nil || started.Status != StatusInProgress {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	evidenceRequired, _, err := restarted.ProviderTransition(provider, "booking-evidence-postgres-01", created.ID, started.Revision, StatusCompletionEvidenceRequired, "Work finished")
	if err != nil {
		t.Fatal(err)
	}
	completed, _, err := restarted.Complete(provider, "booking-complete-postgres-01", created.ID, evidenceRequired.Revision, bookingAssetOne)
	if err != nil || completed.CompletionEvidence == nil || completed.Status != StatusCompletedPendingConfirmation {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	confirmed, _, err := restarted.ConfirmCompletion(winningScope, "booking-confirm-postgres-001", created.ID, completed.Revision)
	if err != nil || confirmed.Status != StatusCompleted {
		t.Fatalf("confirmed=%#v err=%v", confirmed, err)
	}
	wrongTenant := Actor{TenantID: bookingTenantTwo, Country: "IN", Subject: winningScope.CustomerID, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Get(wrongTenant, created.ID); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("cross-tenant get=%v", err)
	}
	var bookingCount, timelineCount, replayCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM booking.bookings`).Scan(&bookingCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM booking.timeline_events WHERE booking_id=$1`, created.ID).Scan(&timelineCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM booking.idempotency_records`).Scan(&replayCount); err != nil {
		t.Fatal(err)
	}
	if bookingCount != 1 || timelineCount != 9 || replayCount < 10 {
		t.Fatalf("booking rows=%d timeline=%d replay=%d", bookingCount, timelineCount, replayCount)
	}
}

func seedPostgresBooking(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO booking.offerings
		(id,tenant_id,country,provider_id,category_id,name,summary,duration_minutes,price_minor,advance_minor,currency,payment_mode,cancellation_policy_ref,reschedule_policy_ref,active,revision,created_at,updated_at,provider_name,verified_provider,rating_average,completed_bookings,live_engagements)
		VALUES ($1,$2,'IN',$3,$4,'Home electrical repair','Verified local electrician',60,50000,10000,'INR','FULL','cancel-v1','reschedule-v1',true,1,$5,$5,'P4U Verified Services',true,4.8,120,3)`, bookingOfferingOne, bookingTenantOne, bookingProviderOne, bookingCategoryOne, now)
	if err == nil {
		_, err = pool.Exec(ctx, `INSERT INTO booking.service_zones (offering_id,postal_code) VALUES ($1,'600001')`, bookingOfferingOne)
	}
	if err == nil {
		_, err = pool.Exec(ctx, `INSERT INTO booking.slots (id,offering_id,provider_id,starts_at,ends_at,timezone,capacity,buffer_minutes,policy_version,provider_revision) VALUES ($1,$2,$3,$4,$5,'Asia/Kolkata',1,15,'policy-v1',1),($6,$2,$3,$7,$8,'Asia/Kolkata',2,15,'policy-v1',1)`, bookingSlotOne, bookingOfferingOne, bookingProviderOne, now.Add(24*time.Hour), now.Add(25*time.Hour), bookingSlotTwo, now.Add(26*time.Hour), now.Add(27*time.Hour))
	}
	if err == nil {
		_, err = pool.Exec(ctx, `INSERT INTO booking.policies (tenant_id,country,version,hold_ttl_seconds,cancellation_cutoff_seconds,maximum_free_reschedules,start_otp_validity_seconds,completion_confirm_seconds,wallet_point_value_minor,updated_at) VALUES ($1,'IN','policy-v1',600,3600,2,86400,172800,100,$2)`, bookingTenantOne, now)
	}
	if err != nil {
		t.Fatal(err)
	}
}

type bookingPaymentFake struct {
	mu      sync.Mutex
	clock   func() time.Time
	values  map[string]payment.Payment
	creates int
}

func newBookingPaymentFake(now time.Time) *bookingPaymentFake {
	return &bookingPaymentFake{clock: func() time.Time { return now }, values: map[string]payment.Payment{}}
}

func (fake *bookingPaymentFake) Create(scope payment.Scope, key, reference string, method payment.Method, amount payment.Money) (payment.Payment, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	if value, exists := fake.values[id]; exists {
		return value, true, nil
	}
	fake.creates++
	value := payment.Payment{ID: id, OrderReference: reference, Method: method, Status: payment.StatusCaptured, Amount: amount, CreatedAt: fake.clock(), UpdatedAt: fake.clock()}
	fake.values[id] = value
	return value, false, nil
}

func (fake *bookingPaymentFake) Get(_ payment.Scope, id string) (payment.Payment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value, exists := fake.values[id]
	if !exists {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	return value, nil
}

func (fake *bookingPaymentFake) RequestRefund(_ payment.Scope, id string, _ payment.Money) (payment.Payment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value := fake.values[id]
	value.Status = payment.StatusRefundSubmitted
	fake.values[id] = value
	return value, nil
}

func (fake *bookingPaymentFake) CancelUncaptured(_ payment.Scope, id string) (payment.Payment, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value := fake.values[id]
	value.Status = payment.StatusCancelled
	fake.values[id] = value
	return value, nil
}

func (fake *bookingPaymentFake) CreateCalls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.creates
}

type bookingWalletFake struct {
	mu      sync.Mutex
	redeems int
}

func (fake *bookingWalletFake) Redeem(_ wallet.Scope, key, _ string, points int64) (wallet.LedgerEntry, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.redeems++
	return wallet.LedgerEntry{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), DeltaPoints: -points}, false, nil
}

func (fake *bookingWalletFake) ReverseDebit(_ wallet.Scope, key, _, _ string) (wallet.LedgerEntry, bool, error) {
	return wallet.LedgerEntry{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()}, false, nil
}

func (fake *bookingWalletFake) RedeemCalls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.redeems
}

func stringsForKey(value string) string {
	value = string([]byte(value))
	return "postgres-" + value + "-0001"
}

const (
	bookingTenantOne   = "e2cda421-4a34-445f-85be-74e93501d184"
	bookingTenantTwo   = "5be35137-d605-4369-adbd-5cdb4a0f3401"
	bookingCustomerOne = "0d45422b-c657-40d5-9f88-32be2b88c9ca"
	bookingCustomerTwo = "2c86bef8-f0b7-4b9a-89cb-b012895a47e7"
	bookingProviderOne = "ece509ab-a3fe-4ffd-a3bf-06d81b8ead38"
	bookingCategoryOne = "bd83b1cb-dce0-4e75-8658-216f5bb0a0e6"
	bookingOfferingOne = "4cd36ca5-ceb0-44d4-8b82-a6a70b33e8d6"
	bookingSlotOne     = "e171556b-b9bc-45f4-8984-38c904ea249e"
	bookingSlotTwo     = "12276a34-a8a9-4f24-81df-380eaf8a7bb6"
	bookingAssetOne    = "05564844-c270-4389-a404-c3684f578d2a"
)
