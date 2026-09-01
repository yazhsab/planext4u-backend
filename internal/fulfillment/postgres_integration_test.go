//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	fulfillmentTenantOne   = "10000000-0000-4000-8000-000000000001"
	fulfillmentTenantTwo   = "10000000-0000-4000-8000-000000000002"
	fulfillmentRiderOne    = "20000000-0000-4000-8000-000000000001"
	fulfillmentRiderTwo    = "20000000-0000-4000-8000-000000000002"
	fulfillmentAdmin       = "30000000-0000-4000-8000-000000000001"
	fulfillmentDispatch    = "30000000-0000-4000-8000-000000000002"
	fulfillmentCustomer    = "40000000-0000-4000-8000-000000000001"
	fulfillmentVendor      = "40000000-0000-4000-8000-000000000002"
	fulfillmentFinanceOne  = "50000000-0000-4000-8000-000000000001"
	fulfillmentFinanceTwo  = "50000000-0000-4000-8000-000000000002"
	fulfillmentWorker      = "50000000-0000-4000-8000-000000000003"
	fulfillmentField       = "60000000-0000-4000-8000-000000000001"
	fulfillmentTerritory   = "70000000-0000-4000-8000-000000000001"
	fulfillmentFranchise   = "70000000-0000-4000-8000-000000000002"
	fulfillmentTask        = "80000000-0000-4000-8000-000000000001"
	fulfillmentOrder       = "90000000-0000-4000-8000-000000000001"
	fulfillmentPhoto       = "a0000000-0000-4000-8000-000000000001"
	fulfillmentDocumentOne = "a0000000-0000-4000-8000-000000000002"
	fulfillmentDocumentTwo = "a0000000-0000-4000-8000-000000000003"
)

func TestPostgresFulfillmentConcurrentAcceptanceRestartLifecycleAndControls(t *testing.T) {
	databaseURL := os.Getenv("FULFILLMENT_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("FULFILLMENT_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS fulfillment CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS fulfillment CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000002_durable_driver_runtime.up.sql",
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
	seedPostgresFulfillment(t, ctx, pool, now)
	service, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	admin := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentAdmin, Roles: []string{"OPS_ADMIN"}, MFAVerified: true}
	riders := []Actor{
		{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentRiderOne, Roles: []string{"RIDER"}},
		{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentRiderTwo, Roles: []string{"RIDER"}},
	}
	for index, rider := range riders {
		profile, replay, err := service.RegisterRider(rider, "rider-registration-postgres-000"+string(rune('1'+index)), RiderRegistrationRequest{FullName: "Production Rider", PhoneMasked: "****1234", VehicleType: "BIKE", VehicleNumber: "TN01AB1234", Documents: []RiderDocument{{Kind: "IDENTITY", AssetID: fulfillmentDocumentOne}, {Kind: "LICENSE", AssetID: fulfillmentDocumentTwo}}, BankReference: "bankref_production001", Zones: []string{"600001"}})
		if err != nil || replay || profile.Status != RiderKYCReview {
			t.Fatalf("register rider %d value=%#v replay=%t err=%v", index, profile, replay, err)
		}
		profile, replay, err = service.ReviewRider(admin, "rider-review-postgres-000000"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Verified production onboarding evidence")
		if err != nil || replay || profile.Status != RiderApproved || profile.BankStatus != "VERIFIED" {
			t.Fatalf("review rider %d value=%#v replay=%t err=%v", index, profile, replay, err)
		}
		duty, replay, err := service.StartDuty(rider, "rider-duty-start-postgres-000"+string(rune('1'+index)), "600001")
		if err != nil || replay || duty.Status != "ACTIVE" {
			t.Fatalf("start duty %d value=%#v replay=%t err=%v", index, duty, replay, err)
		}
	}

	dispatch := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentDispatch, Roles: []string{"DISPATCH"}, MFAVerified: true}
	task, err := service.SeedTask(dispatch, TaskSeed{ID: fulfillmentTask, OrderID: fulfillmentOrder, OrderType: "PRODUCT", RegionID: "CHENNAI", TerritoryID: fulfillmentTerritory, ZoneID: "600001", Pickup: Stop{Label: "Vendor", AddressToken: "pickup_token", Point: Point{Latitude: 13.0827, Longitude: 80.2707}}, Dropoff: Stop{Label: "Customer", AddressToken: "dropoff_token", Point: Point{Latitude: 13.083, Longitude: 80.271}}, DistanceMeters: 1200, Earning: Money{AmountMinor: 15000, Currency: "INR"}, DeliveryOTP: "123456", CustomerID: fulfillmentCustomer, CounterpartyID: fulfillmentVendor})
	if err != nil || task.Status != "READY_FOR_DISPATCH" {
		t.Fatalf("seed task=%#v err=%v", task, err)
	}
	task, _, err = service.OfferTask(dispatch, "dispatch-offer-postgres-0001", task.ID, task.Revision)
	if err != nil || task.Status != "OFFERED" {
		t.Fatalf("offer task=%#v err=%v", task, err)
	}

	type acceptance struct {
		rider Actor
		task  DeliveryTask
		err   error
	}
	results := make(chan acceptance, 2)
	var wait sync.WaitGroup
	for index, rider := range riders {
		wait.Add(1)
		go func(index int, rider Actor) {
			defer wait.Done()
			value, _, callErr := service.AcceptOffer(rider, "rider-accept-postgres-00000"+string(rune('1'+index)), task.ID, task.Revision)
			results <- acceptance{rider: rider, task: value, err: callErr}
		}(index, rider)
	}
	wait.Wait()
	close(results)
	var winner Actor
	var assigned DeliveryTask
	succeeded, conflicted := 0, 0
	for result := range results {
		if result.err == nil {
			succeeded++
			winner, assigned = result.rider, result.task
		} else if errors.Is(result.err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected accept error: %v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 || assigned.AssignedRiderID != winner.Subject {
		t.Fatalf("accept race succeeded=%d conflicted=%d assigned=%#v", succeeded, conflicted, assigned)
	}

	restarted, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	winnerIndex := 0
	if winner.Subject == fulfillmentRiderTwo {
		winnerIndex = 1
	}
	replayed, replay, err := restarted.AcceptOffer(winner, "rider-accept-postgres-00000"+string(rune('1'+winnerIndex)), task.ID, task.Revision)
	if err != nil || !replay || replayed.AssignedRiderID != winner.Subject {
		t.Fatalf("restart replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	location, replay, err := restarted.UpdateLocation(winner, "rider-location-postgres-0001", LocationUpdate{Sequence: 1, Point: Point{Latitude: 13.0828, Longitude: 80.2708}, AccuracyM: 8, CapturedAt: now})
	if err != nil || replay || location.Sequence != 1 {
		t.Fatalf("location=%#v replay=%t err=%v", location, replay, err)
	}
	pickedUp, _, err := restarted.MarkPickedUp(winner, "rider-pickup-postgres-00001", assigned.ID, assigned.Revision)
	if err != nil || pickedUp.Status != "PICKED_UP" {
		t.Fatalf("pickup=%#v err=%v", pickedUp, err)
	}
	delivered, _, err := restarted.CompleteDelivery(winner, "rider-complete-postgres-0001", pickedUp.ID, pickedUp.Revision, CompletionRequest{OTP: "123456", BlurredPhotoAssetID: fulfillmentPhoto})
	if err != nil || delivered.Status != "DELIVERED" {
		t.Fatalf("delivered=%#v err=%v", delivered, err)
	}
	ledger, err := restarted.Ledger(winner, winner.Subject)
	if err != nil || len(ledger) != 1 || ledger[0].Net.AmountMinor != 14213 {
		t.Fatalf("ledger=%#v err=%v", ledger, err)
	}

	customer := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentCustomer, Roles: []string{"CUSTOMER"}}
	conversation, err := restarted.Conversation(customer, fulfillmentOrder)
	if err != nil || len(conversation.ParticipantIDs) != 3 {
		t.Fatalf("conversation=%#v err=%v", conversation, err)
	}
	message, replay, err := restarted.SendMessage(customer, "chat-message-postgres-000001", conversation.ID, "Call me at +91 99999 99999")
	if err != nil || replay || !message.Redacted || message.Body == "Call me at +91 99999 99999" {
		t.Fatalf("message=%#v replay=%t err=%v", message, replay, err)
	}
	message, replay, err = restarted.MessageReceipt(winner, "chat-receipt-postgres-000001", conversation.ID, message.ID, "READ")
	if err != nil || replay || len(message.Receipts) != 2 {
		t.Fatalf("receipt=%#v replay=%t err=%v", message, replay, err)
	}

	payout, replay, err := restarted.RequestPayout(winner, "payout-request-postgres-0001", []string{ledger[0].ID})
	if err != nil || replay || payout.Status != "PENDING_REVIEW" {
		t.Fatalf("payout=%#v replay=%t err=%v", payout, replay, err)
	}
	financeOne := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentFinanceOne, Roles: []string{"FINANCE"}, MFAVerified: true}
	financeTwo := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentFinanceTwo, Roles: []string{"FINANCE"}, MFAVerified: true}
	payout, _, err = restarted.ApprovePayout(financeOne, "payout-approve-postgres-001", payout.ID, payout.Revision, "First finance approval completed")
	if err != nil || payout.Status != "FIRST_APPROVED" {
		t.Fatalf("first approval=%#v err=%v", payout, err)
	}
	payout, _, err = restarted.ApprovePayout(financeTwo, "payout-approve-postgres-002", payout.ID, payout.Revision, "Second finance approval completed")
	if err != nil || payout.Status != "APPROVED" {
		t.Fatalf("second approval=%#v err=%v", payout, err)
	}
	worker := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentWorker, Roles: []string{"PAYOUT_WORKER"}}
	payout, _, err = restarted.ExecutePayout(worker, "payout-execute-postgres-001", payout.ID, payout.Revision, "provider_ref_0001", true)
	if err != nil || payout.Status != "PAID" {
		t.Fatalf("execute=%#v err=%v", payout, err)
	}
	reconciliation, err := restarted.Reconcile(financeOne, winner.Subject)
	if err != nil || reconciliation.VarianceMinor != 0 || reconciliation.PaidMinor != ledger[0].Net.AmountMinor {
		t.Fatalf("reconciliation=%#v err=%v", reconciliation, err)
	}

	field := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentField, Roles: []string{"FIELD_OFFICER"}}
	checkIn, replay, err := restarted.FieldCheckIn(field, "field-checkin-postgres-0001", fulfillmentTerritory, Point{Latitude: 13.0827, Longitude: 80.2707})
	if err != nil || replay || checkIn.DistanceM > 1 {
		t.Fatalf("check-in=%#v replay=%t err=%v", checkIn, replay, err)
	}
	attendance, err := restarted.Attendance(admin, fulfillmentField)
	if err != nil || len(attendance) != 1 {
		t.Fatalf("attendance=%#v err=%v", attendance, err)
	}
	dashboard, err := restarted.RegionalDashboard(admin, "CHENNAI")
	if err != nil || dashboard.Territories != 1 || dashboard.CompletedToday != 1 || len(dashboard.RecentFieldCheckIns) != 1 {
		t.Fatalf("dashboard=%#v err=%v", dashboard, err)
	}

	wrongTenant := Actor{TenantID: fulfillmentTenantTwo, Country: "IN", Subject: fulfillmentCustomer, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Conversation(wrongTenant, fulfillmentOrder); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant conversation error=%v", err)
	}
	var tasks, conversations, earnings, claims, replayRows int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM fulfillment.delivery_tasks),(SELECT count(*) FROM fulfillment.conversations),(SELECT count(*) FROM fulfillment.ledger_entries),(SELECT count(*) FROM fulfillment.payout_entry_claims),(SELECT count(*) FROM fulfillment.idempotency_records)`).Scan(&tasks, &conversations, &earnings, &claims, &replayRows); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || conversations != 1 || earnings != 1 || claims != 1 || replayRows < 15 {
		t.Fatalf("rows tasks=%d chats=%d earnings=%d claims=%d replays=%d", tasks, conversations, earnings, claims, replayRows)
	}
}

func seedPostgresFulfillment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO fulfillment.policies (tenant_id,country,version,offer_ttl_seconds,location_ttl_seconds,chat_after_delivery_seconds,settlement_cooling_seconds,commission_basis_points,tax_basis_points,updated_at)
		VALUES ($1,'IN','fulfillment-v1',300,120,7200,0,500,500,$2)`, fulfillmentTenantOne, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO fulfillment.territories (id,tenant_id,country,region_id,franchise_identity_id,name,center_latitude,center_longitude,radius_km,postal_codes)
		VALUES ($1,$2,'IN','CHENNAI',$3,'Chennai Central',13.0827,80.2707,5,ARRAY['600001'])`, fulfillmentTerritory, fulfillmentTenantOne, fulfillmentFranchise)
	if err != nil {
		t.Fatal(err)
	}
}
