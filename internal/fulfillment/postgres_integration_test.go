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
	"github.com/yazhsab/planext4u-backend/internal/bankverification"
	"github.com/yazhsab/planext4u-backend/internal/kycattestation"
	"github.com/yazhsab/planext4u-backend/internal/privateevidence"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
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
		"../../migrations/platform/000008_admin_roles.up.sql",
		"../../migrations/fulfillment/000001_fulfillment.up.sql",
		"../../migrations/fulfillment/000002_durable_driver_runtime.up.sql",
		"../../migrations/fulfillment/000004_offer_declines.up.sql",
		"../../migrations/fulfillment/000005_scoped_recovery.up.sql",
		"../../migrations/fulfillment/000008_rider_rework.up.sql",
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
	prepareDeliveryConsents(t, ctx, pool, now)
	testsupport.PrivateIdentityMedia(t, ctx, pool, fulfillmentTenantOne, fulfillmentRiderOne, []string{fulfillmentDocumentOne, fulfillmentDocumentTwo, "a0000000-0000-4000-8000-000000000006", "a0000000-0000-4000-8000-000000000007"})
	testsupport.AddPrivateIdentityMedia(t, ctx, pool, fulfillmentTenantOne, fulfillmentRiderTwo, []string{"a0000000-0000-4000-8000-000000000004", "a0000000-0000-4000-8000-000000000005", "a0000000-0000-4000-8000-000000000008", "a0000000-0000-4000-8000-000000000009"})
	owned := map[string][]RiderDocument{
		fulfillmentRiderOne: {{Kind: "IDENTITY", AssetID: fulfillmentDocumentOne}, {Kind: "DRIVER_LICENSE", AssetID: fulfillmentDocumentTwo}, {Kind: "VEHICLE_REGISTRATION", AssetID: "a0000000-0000-4000-8000-000000000006"}, {Kind: "INSURANCE", AssetID: "a0000000-0000-4000-8000-000000000007"}},
		fulfillmentRiderTwo: {{Kind: "IDENTITY", AssetID: "a0000000-0000-4000-8000-000000000004"}, {Kind: "DRIVER_LICENSE", AssetID: "a0000000-0000-4000-8000-000000000005"}, {Kind: "VEHICLE_REGISTRATION", AssetID: "a0000000-0000-4000-8000-000000000008"}, {Kind: "INSURANCE", AssetID: "a0000000-0000-4000-8000-000000000009"}},
	}
	denyEvidence := false
	evidence := privateevidence.VerifyFunc(func(_ context.Context, scope privateevidence.Scope, ids []string) error {
		if denyEvidence {
			return privateevidence.ErrRejected
		}
		if scope.TenantID != fulfillmentTenantOne || scope.Country != "IN" {
			return privateevidence.ErrRejected
		}
		expected := owned[scope.OwnerID]
		if len(expected) != len(ids) {
			return privateevidence.ErrRejected
		}
		for _, id := range ids {
			found := false
			for _, doc := range expected {
				if id == doc.AssetID {
					found = true
				}
			}
			if !found {
				return privateevidence.ErrRejected
			}
		}
		return nil
	})
	denyBank := false
	bank := bankverification.VerifyFunc(func(_ context.Context, scope bankverification.Scope, account bankverification.Account) (string, error) {
		if denyBank || scope.TenantID != fulfillmentTenantOne || scope.Country != "IN" || owned[scope.OwnerID] == nil || account.Reference != "bankref_"+scope.OwnerID {
			return "", bankverification.ErrRejected
		}
		return "synthetic_bank_verification_01", nil
	})
	denyKYC := false
	kyc := kycattestation.VerifyFunc(func(_ context.Context, scope kycattestation.Scope, documents []kycattestation.Document) (string, error) {
		expected := owned[scope.OwnerID]
		if denyKYC || scope.TenantID != fulfillmentTenantOne || scope.Country != "IN" || len(expected) == 0 || len(documents) != len(expected) {
			return "", kycattestation.ErrRejected
		}
		for index, document := range documents {
			if document.Kind != expected[index].Kind || document.AssetID != expected[index].AssetID {
				return "", kycattestation.ErrRejected
			}
		}
		return "synthetic_kyc_attestation_01", nil
	})
	service, err := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank, kyc)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	admin := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentAdmin, Roles: []string{"OPS_ADMIN"}, MFAVerified: true}
	admin.OperatorRevision = 1
	admin.OperatorSessionID = testsupport.ParticipantReviewer(t, ctx, pool, fulfillmentTenantOne, fulfillmentAdmin)
	riders := []Actor{
		{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentRiderOne, Roles: []string{"RIDER"}},
		{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentRiderTwo, Roles: []string{"RIDER"}},
	}
	for index := range riders {
		riders[index].SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, riders[index].TenantID, riders[index].Subject, "RIDER")
	}
	withoutSource := riders[0]
	withoutSource.SourceSessionID = ""
	if _, _, err := service.RegisterRider(withoutSource, "no-source-rider-register-01", RiderRegistrationRequest{FullName: "Fresh rider", PhoneMasked: "****1234", VehicleType: "BIKE", VehicleNumber: "TN01AB1234", Documents: owned[withoutSource.Subject], BankReference: "bankref_production001", Zones: []string{"600001"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rider registration without current source session: %v", err)
	}
	unconfigured, _ := NewPostgresService(pool, func() time.Time { return now })
	request := RiderRegistrationRequest{FullName: "Fresh rider", PhoneMasked: "****1234", VehicleType: "BIKE", VehicleNumber: "TN01AB1234", Documents: owned[fulfillmentRiderOne], BankReference: "bankref_production001", Zones: []string{"600001"}}
	if _, _, err := unconfigured.RegisterRider(riders[0], "no-verifier-register-pg1", request); !errors.Is(err, privateevidence.ErrUnavailable) {
		t.Fatalf("missing verifier accepted: %v", err)
	}
	if _, _, err := service.RegisterRider(riders[1], "foreign-docs-register-pg1", request); !errors.Is(err, privateevidence.ErrRejected) {
		t.Fatalf("cross-owner documents accepted: %v", err)
	}
	if _, err := service.Rider(riders[1]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed evidence created rider: %v", err)
	}
	for index, rider := range riders {
		profile, replay, err := service.RegisterRider(rider, "rider-registration-postgres-000"+string(rune('1'+index)), RiderRegistrationRequest{FullName: "Production Rider", PhoneMasked: "****1234", VehicleType: "BIKE", VehicleNumber: "TN01AB1234", Documents: owned[rider.Subject], BankReference: "bankref_" + rider.Subject, Zones: []string{"600001"}})
		if err != nil || replay || profile.Status != RiderKYCReview {
			t.Fatalf("register rider %d value=%#v replay=%t err=%v", index, profile, replay, err)
		}
		denyEvidence = true
		if _, _, err := service.ReviewRider(admin, "rider-recheck-rejected-pg"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Document no longer available at review"); !errors.Is(err, privateevidence.ErrRejected) {
			t.Fatalf("rider approval skipped evidence: %v", err)
		}
		if unchanged, err := service.Rider(rider); err != nil || unchanged.Status != RiderKYCReview || unchanged.Revision != profile.Revision {
			t.Fatalf("failed review mutated rider: %+v %v", unchanged, err)
		}
		denyEvidence = false
		withoutKYC, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank)
		if _, _, err := withoutKYC.ReviewRider(admin, "rider-no-kyc-provider-pg"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "READY media is not identity attestation"); !errors.Is(err, kycattestation.ErrUnavailable) {
			t.Fatalf("missing KYC proof approved rider: %v", err)
		}
		denyKYC = true
		if _, _, err := service.ReviewRider(admin, "rider-denied-kyc-provider-pg"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Rejected KYC proof must not approve"); !errors.Is(err, kycattestation.ErrRejected) {
			t.Fatalf("rejected KYC proof approved rider: %v", err)
		}
		if unchanged, err := service.Rider(rider); err != nil || unchanged.Revision != profile.Revision || unchanged.Status != RiderKYCReview || unchanged.Documents[0].OCRStatus == "VERIFIED" {
			t.Fatalf("failed KYC proof mutated rider: %+v %v", unchanged, err)
		}
		denyKYC = false
		noBank, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bankverification.Unavailable(), kyc)
		if _, _, err := noBank.ReviewRider(admin, "rider-no-bank-provider-pg"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Missing bank provider must not approve"); !errors.Is(err, bankverification.ErrUnavailable) {
			t.Fatalf("missing bank proof approved rider: %v", err)
		}
		denyBank = true
		if _, _, err := service.ReviewRider(admin, "rider-denied-bank-provider-pg"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Rejected bank proof must not approve"); !errors.Is(err, bankverification.ErrRejected) {
			t.Fatalf("rejected bank approved rider: %v", err)
		}
		if unchanged, err := service.Rider(rider); err != nil || unchanged.Revision != profile.Revision || unchanged.Status != RiderKYCReview || unchanged.BankStatus != "PENDING_VERIFICATION" {
			t.Fatalf("failed bank proof mutated rider: %+v %v", unchanged, err)
		}
		denyBank = false
		profile, replay, err = service.ReviewRider(admin, "rider-review-postgres-000000"+string(rune('1'+index)), rider.Subject, profile.Revision, true, "Verified production onboarding evidence")
		if err != nil || replay || profile.Status != RiderApproved || profile.BankStatus != "VERIFIED" {
			t.Fatalf("review rider %d value=%#v replay=%t err=%v", index, profile, replay, err)
		}
		var kycAudit int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM fulfillment.audit_events WHERE resource_id=$1 AND action='RIDER_REVIEW' AND position($2 in reason)>0`, rider.Subject, "synthetic_kyc_attestation_01").Scan(&kycAudit); err != nil || kycAudit != 1 {
			t.Fatalf("missing committed rider KYC attestation evidence: count=%d err=%v", kycAudit, err)
		}
		duty, replay, err := service.StartDuty(rider, "rider-duty-start-postgres-000"+string(rune('1'+index)), "600001")
		if err != nil || replay || duty.Status != "ACTIVE" {
			t.Fatalf("start duty %d value=%#v replay=%t err=%v", index, duty, replay, err)
		}
	}

	admin = verifyRiderParticipantReviewAuthority(t, ctx, pool, databaseURL, admin)
	financeOne, financeTwo := prepareFinanceOperatorAuthority(t, ctx, pool)

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

	restarted, err := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank)
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
	trackingCustomer := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentCustomer, Roles: []string{"CUSTOMER"}}
	if _, err := restarted.Location(trackingCustomer, winner.Subject); err != nil {
		t.Fatalf("order party tracking=%v", err)
	}
	for _, subject := range []string{fulfillmentRiderOne, fulfillmentRiderTwo} {
		unrelated := trackingCustomer
		unrelated.Subject = subject
		if _, err := restarted.Location(unrelated, winner.Subject); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unrelated or chat-history tracking=%v", err)
		}
	}
	// Acknowledged sequence watermarks survive both an empty client queue
	// and an independent service restart. Another rider remains at sequence zero.
	offline := OfflineCommand{DeviceSequence: 1, CommandID: "offline-location-postgres-001", Kind: "LOCATION", Payload: map[string]any{"latitude": 13.0828, "longitude": 80.2708, "accuracy_m": 8, "captured_at": now.Format(time.RFC3339)}}
	if results, err := restarted.RecoverOffline(winner, []OfflineCommand{offline}); err != nil || len(results) != 1 {
		t.Fatalf("offline results=%#v err=%v", results, err)
	}
	if _, err := restarted.RecoverOffline(winner, []OfflineCommand{offline}); err != nil {
		t.Fatal(err)
	}
	nextOffline := offline
	nextOffline.DeviceSequence = 2
	nextOffline.CommandID = "offline-location-postgres-002"
	if results, err := restarted.RecoverOffline(winner, []OfflineCommand{offline, nextOffline}); err != nil || len(results) != 2 {
		t.Fatalf("partially acknowledged replay=%#v err=%v", results, err)
	}
	negotiated, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank)
	profile, err := negotiated.Rider(winner)
	if err != nil || profile.LastCommandSequence != 2 || profile.LastLocationSequence != 3 {
		t.Fatalf("watermarks=%#v err=%v", profile, err)
	}
	other := riders[1-winnerIndex]
	otherProfile, err := negotiated.Rider(other)
	if err != nil || otherProfile.LastCommandSequence != 0 || otherProfile.LastLocationSequence != 0 {
		t.Fatalf("other watermarks=%#v err=%v", otherProfile, err)
	}
	if _, _, err := negotiated.UpdateLocation(winner, "location-after-restart-001", LocationUpdate{Sequence: profile.LastLocationSequence + 1, Point: Point{Latitude: 13.0828, Longitude: 80.2708}, AccuracyM: 8, CapturedAt: now}); err != nil {
		t.Fatal(err)
	}
	pickedUp, _, err := restarted.MarkPickedUp(winner, "rider-pickup-postgres-00001", assigned.ID, assigned.Revision)
	verifyDeliveryConsentEnforcement(t, ctx, pool, restarted, winner, now)
	if err != nil || pickedUp.Status != "PICKED_UP" {
		t.Fatalf("pickup=%#v err=%v", pickedUp, err)
	}
	unknownPickup := OfflineCommand{DeviceSequence: 3, CommandID: "offline-pickup-lost-response-001", Kind: "PICKUP", TaskID: assigned.ID, Revision: assigned.Revision}
	if results, err := restarted.RecoverOffline(winner, []OfflineCommand{unknownPickup}); err != nil || len(results) != 1 || results[0].Status != "APPLIED" {
		t.Fatalf("lost-response recovery=%#v err=%v", results, err)
	}
	if tasks, err := restarted.Tasks(winner); err != nil || len(tasks) != 1 || tasks[0].Revision != pickedUp.Revision {
		t.Fatalf("recovery repeated pickup: %#v %v", tasks, err)
	}
	delivered, _, err := restarted.CompleteDelivery(winner, "rider-complete-postgres-0001", pickedUp.ID, pickedUp.Revision, CompletionRequest{OTP: "123456", BlurredPhotoAssetID: fulfillmentPhoto})
	if err != nil || delivered.Status != "DELIVERED" {
		t.Fatalf("delivered=%#v err=%v", delivered, err)
	}
	if _, err := restarted.Location(trackingCustomer, winner.Subject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tracking after completion=%v", err)
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
	verifySelfSettlementAuthority(t, ctx, pool, restarted, winner, ledger[0].ID, payout.ID, now)
	verifyFinanceReadAuthority(t, ctx, pool, restarted, financeOne, admin, winner.Subject, payout.ID, payout.Revision)
	payout, _, err = restarted.ApprovePayout(financeOne, "payout-approve-postgres-001", payout.ID, payout.Revision, "First finance approval completed")
	if err != nil || payout.Status != "FIRST_APPROVED" {
		t.Fatalf("first approval=%#v err=%v", payout, err)
	}
	verifyFinanceRevokedReplay(t, ctx, pool, restarted, financeOne, payout.ID)
	financeTwo = verifyFinanceQueuedRevocation(t, ctx, pool, restarted, financeTwo, payout.ID, payout.Revision)
	payout, _, err = restarted.ApprovePayout(financeTwo, "payout-approve-postgres-002", payout.ID, payout.Revision, "Second finance approval completed")
	if err != nil || payout.Status != "APPROVED" {
		t.Fatalf("second approval=%#v err=%v", payout, err)
	}
	worker := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentWorker, Roles: []string{"PAYOUT_WORKER"}}
	payout, _, err = restarted.ExecutePayout(worker, "payout-execute-postgres-001", payout.ID, payout.Revision, "provider_ref_0001", true)
	if err != nil || payout.Status != "PAID" {
		t.Fatalf("execute=%#v err=%v", payout, err)
	}
	reconciliation, err := restarted.Reconcile(financeTwo, winner.Subject)
	if err != nil || reconciliation.VarianceMinor != 0 || reconciliation.PaidMinor != ledger[0].Net.AmountMinor {
		t.Fatalf("reconciliation=%#v err=%v", reconciliation, err)
	}

	verifyTerritoryAccessFoundation(t, ctx, pool, now, admin)

	field := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentField, Roles: []string{"FIELD_OFFICER"}, MFAVerified: true, OperatorSessionID: fieldOperatorCurrentSession, OperatorRevision: 3}
	checkIn, replay, err := restarted.FieldCheckIn(field, "field-checkin-postgres-0001", fulfillmentTerritory, Point{Latitude: 13.0827, Longitude: 80.2707})
	if err != nil || replay || checkIn.DistanceM > 1 {
		t.Fatalf("check-in=%#v replay=%t err=%v", checkIn, replay, err)
	}
	attendance, err := restarted.Attendance(admin, fulfillmentField)
	if err != nil || len(attendance) != 1 {
		t.Fatalf("attendance=%#v err=%v", attendance, err)
	}
	visibleTerritories, err := restarted.Territories(admin)
	if err != nil || len(visibleTerritories) != 1 {
		t.Fatalf("current country operator territories=%+v err=%v", visibleTerritories, err)
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

	declineTask, err := restarted.SeedTask(dispatch, postgresFulfillmentTaskSeed("80000000-0000-4000-8000-000000000011", "90000000-0000-4000-8000-000000000011"))
	if err != nil {
		t.Fatal(err)
	}
	declineTask, _, err = restarted.OfferTask(dispatch, "dispatch-decline-postgres-001", declineTask.ID, declineTask.Revision)
	if err != nil || !contains(declineTask.AllowedActions, "DECLINE") {
		t.Fatalf("decline offer=%#v err=%v", declineTask, err)
	}
	declineRequest := OfferDeclineRequest{ReasonCode: "TOO_FAR"}
	declined, replay, err := restarted.DeclineOffer(riders[0], "rider-decline-postgres-0001", declineTask.ID, declineTask.Revision, declineRequest)
	if err != nil || replay || declined.TaskID != declineTask.ID {
		t.Fatalf("declined=%#v replay=%t err=%v", declined, replay, err)
	}
	afterDeclineRestart, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now }, evidence, bank)
	declinedAgain, replay, err := afterDeclineRestart.DeclineOffer(riders[0], "rider-decline-postgres-0001", declineTask.ID, declineTask.Revision, declineRequest)
	if err != nil || !replay || declinedAgain.TaskID != declined.TaskID {
		t.Fatalf("durable decline replay=%#v replay=%t err=%v", declinedAgain, replay, err)
	}
	if offers, err := afterDeclineRestart.Offers(riders[0]); err != nil || len(offers) != 0 {
		t.Fatalf("declining rider offers=%#v err=%v", offers, err)
	}
	if offers, err := afterDeclineRestart.Offers(riders[1]); err != nil || len(offers) != 1 {
		t.Fatalf("other rider offers=%#v err=%v", offers, err)
	}
	if _, _, err := afterDeclineRestart.AcceptOffer(riders[0], "rider-accept-after-decline-pg", declineTask.ID, declineTask.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("same rider accepted declined offer: %v", err)
	}
	acceptedByOther, _, err := afterDeclineRestart.AcceptOffer(riders[1], "rider-other-accept-postgres", declineTask.ID, declineTask.Revision)
	if err != nil || acceptedByOther.AssignedRiderID != riders[1].Subject {
		t.Fatalf("other rider acceptance=%#v err=%v", acceptedByOther, err)
	}

	expiredTask, _ := restarted.SeedTask(dispatch, postgresFulfillmentTaskSeed("80000000-0000-4000-8000-000000000012", "90000000-0000-4000-8000-000000000012"))
	expiredTask, _, _ = restarted.OfferTask(dispatch, "dispatch-expired-postgres-001", expiredTask.ID, expiredTask.Revision)
	expiredService, _ := NewPostgresServiceWithProviders(pool, func() time.Time { return now.Add(301 * time.Second) }, evidence, bank)
	if _, _, err := expiredService.DeclineOffer(riders[0], "rider-expired-postgres-0001", expiredTask.ID, expiredTask.Revision, OfferDeclineRequest{ReasonCode: "ENDING_DUTY"}); !errors.Is(err, ErrOfferExpired) {
		t.Fatalf("expired postgres decline error=%v", err)
	}

	raceTask, _ := restarted.SeedTask(dispatch, postgresFulfillmentTaskSeed("80000000-0000-4000-8000-000000000013", "90000000-0000-4000-8000-000000000013"))
	raceTask, _, _ = restarted.OfferTask(dispatch, "dispatch-race-postgres-0001", raceTask.ID, raceTask.Revision)
	decisionResults := make(chan error, 2)
	wait = sync.WaitGroup{}
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, _, callErr := restarted.AcceptOffer(riders[0], "rider-race-accept-postgres", raceTask.ID, raceTask.Revision)
		decisionResults <- callErr
	}()
	go func() {
		defer wait.Done()
		_, _, callErr := restarted.DeclineOffer(riders[0], "rider-race-decline-postgres", raceTask.ID, raceTask.Revision, OfferDeclineRequest{ReasonCode: "VEHICLE_OR_CAPACITY"})
		decisionResults <- callErr
	}()
	wait.Wait()
	close(decisionResults)
	decisionSucceeded, decisionConflicted := 0, 0
	for callErr := range decisionResults {
		switch {
		case callErr == nil:
			decisionSucceeded++
		case errors.Is(callErr, ErrConflict):
			decisionConflicted++
		default:
			t.Fatalf("postgres decision race error=%v", callErr)
		}
	}
	if decisionSucceeded != 1 || decisionConflicted != 1 {
		t.Fatalf("postgres decision race succeeded=%d conflicted=%d", decisionSucceeded, decisionConflicted)
	}
}

func postgresFulfillmentTaskSeed(id, orderID string) TaskSeed {
	return TaskSeed{ID: id, OrderID: orderID, OrderType: "PRODUCT", RegionID: "CHENNAI", TerritoryID: fulfillmentTerritory, ZoneID: "600001", Pickup: Stop{Label: "Vendor", AddressToken: "pickup_token", Point: Point{Latitude: 13.0827, Longitude: 80.2707}}, Dropoff: Stop{Label: "Customer", AddressToken: "dropoff_token", Point: Point{Latitude: 13.083, Longitude: 80.271}}, DistanceMeters: 1200, Earning: Money{AmountMinor: 15000, Currency: "INR"}, DeliveryOTP: "123456", CustomerID: fulfillmentCustomer, CounterpartyID: fulfillmentVendor}
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
