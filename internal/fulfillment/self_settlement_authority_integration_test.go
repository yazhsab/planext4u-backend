//go:build integration

package fulfillment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/testsupport"
)

// This runs only in the disposable fulfillment integration database. The
// current participant source session must protect readback and payout replay,
// not just the original settlement mutation.
func verifySelfSettlementAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, rider Actor, riderEntryID, riderPayoutID string, now time.Time) {
	t.Helper()
	if entries, err := service.Ledger(rider, rider.Subject); err != nil || len(entries) != 1 {
		t.Fatalf("current rider ledger: %+v %v", entries, err)
	}
	if payouts, err := service.Payouts(rider, rider.Subject); err != nil || len(payouts) != 1 || payouts[0].ID != riderPayoutID {
		t.Fatalf("current rider payouts: %+v %v", payouts, err)
	}
	if value, replay, err := service.RequestPayout(rider, "payout-request-postgres-0001", []string{riderEntryID}); err != nil || !replay || value.ID != riderPayoutID {
		t.Fatalf("current rider payout replay: %+v %t %v", value, replay, err)
	}

	for name, alter := range map[string]func(*Actor){
		"missing-source":  func(a *Actor) { a.SourceSessionID = "" },
		"unknown-source":  func(a *Actor) { a.SourceSessionID = uuid.NewString() },
		"foreign-country": func(a *Actor) { a.Country = "NG" },
		"foreign-tenant":  func(a *Actor) { a.TenantID = fulfillmentTenantTwo },
		"forged-role":     func(a *Actor) { a.Roles = []string{"VENDOR"} },
	} {
		bad := rider
		alter(&bad)
		assertSelfSettlementDenied(t, service, bad, rider.Subject, riderEntryID, "rider-denied-"+name)
	}
	other := rider
	other.Subject = fulfillmentRiderOne
	if other.Subject == rider.Subject {
		other.Subject = fulfillmentRiderTwo
	}
	assertSelfSettlementDenied(t, service, other, rider.Subject, riderEntryID, "rider-denied-other")
	verifySelfSettlementHTTPDenial(t, service, rider, riderEntryID)

	vendor := Actor{TenantID: fulfillmentTenantOne, Country: "IN", Subject: fulfillmentVendor, Roles: []string{"VENDOR"}}
	vendor.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, vendor.TenantID, vendor.Subject, "VENDOR")
	vendorEntryID, vendorReferenceID := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO fulfillment.ledger_entries(id,tenant_id,country,account_identity_id,reference_id,kind,gross_minor,commission_minor,tax_minor,net_minor,currency,calculation_version,available_at,created_at) VALUES($1,$2,$3,$4,$5,'VENDOR_SALE',1200,100,20,1080,'INR','synthetic-self-authority',$6,$6)`, vendorEntryID, vendor.TenantID, vendor.Country, vendor.Subject, vendorReferenceID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if entries, err := service.Ledger(vendor, vendor.Subject); err != nil || len(entries) != 1 || entries[0].ID != vendorEntryID {
		t.Fatalf("current vendor ledger: %+v %v", entries, err)
	}
	if payouts, err := service.Payouts(vendor, vendor.Subject); err != nil || len(payouts) != 0 {
		t.Fatalf("current vendor empty payout history: %+v %v", payouts, err)
	}
	vendorPayout, replay, err := service.RequestPayout(vendor, "vendor-payout-request-self-001", []string{vendorEntryID})
	if err != nil || replay || vendorPayout.AccountID != vendor.Subject {
		t.Fatalf("current vendor payout request: %+v %t %v", vendorPayout, replay, err)
	}
	if payouts, err := service.Payouts(vendor, vendor.Subject); err != nil || len(payouts) != 1 || payouts[0].ID != vendorPayout.ID {
		t.Fatalf("current vendor payout readback: %+v %v", payouts, err)
	}
	restaurant := vendor
	restaurant.Roles = []string{"RESTAURANT_VENDOR"}
	if entries, err := service.Ledger(restaurant, vendor.Subject); err != nil || len(entries) != 1 {
		t.Fatalf("restaurant vendor mapped to current vendor role: %+v %v", entries, err)
	}
	assertSelfSettlementDenied(t, service, vendor, rider.Subject, riderEntryID, "vendor-cross-party")
	assertSelfSettlementDenied(t, service, rider, vendor.Subject, vendorEntryID, "rider-cross-party")
	forged := vendor
	forged.Roles = []string{"RIDER"}
	assertSelfSettlementDenied(t, service, forged, vendor.Subject, vendorEntryID, "vendor-forged-rider")

	verifyQueuedSelfSettlementRevocation(t, ctx, pool, service, vendor, vendorEntryID)
	assertSelfSettlementDenied(t, service, vendor, vendor.Subject, vendorEntryID, "vendor-revoked-replay")
	vendor.SourceSessionID = testsupport.ParticipantSession(t, ctx, pool, vendor.TenantID, vendor.Subject, "VENDOR")
	requestID := "self-settlement-vendor-due-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO identity.account_deletion_requests(id,identity_id,status,reason,requested_at,effective_at) VALUES($1,$2,'SCHEDULED','synthetic self settlement denial',clock_timestamp()-interval '2 minutes',clock_timestamp()-interval '1 minute')`, requestID, vendor.Subject); err != nil {
		t.Fatal(err)
	}
	assertSelfSettlementDenied(t, service, vendor, vendor.Subject, vendorEntryID, "vendor-due-deletion")
	if _, err := pool.Exec(ctx, `UPDATE identity.account_deletion_requests SET status='CANCELLED' WHERE id=$1`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.identities SET disabled_at=clock_timestamp() WHERE id=$1`, vendor.Subject); err != nil {
		t.Fatal(err)
	}
	assertSelfSettlementDenied(t, service, vendor, vendor.Subject, vendorEntryID, "vendor-disabled")
	if _, err := pool.Exec(ctx, `UPDATE identity.identities SET disabled_at=NULL WHERE id=$1`, vendor.Subject); err != nil {
		t.Fatal(err)
	}
	// The surrounding lifecycle asserts its original financial row and replay
	// counts. Remove only this synthetic vendor fixture before those assertions.
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM fulfillment.payout_entry_claims WHERE payout_id=$1`, []any{vendorPayout.ID}},
		{`DELETE FROM fulfillment.payouts WHERE id=$1`, []any{vendorPayout.ID}},
		{`DELETE FROM fulfillment.ledger_entries WHERE id=$1`, []any{vendorEntryID}},
		{`DELETE FROM fulfillment.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation='payout-request' AND idempotency_key='vendor-payout-request-self-001'`, []any{vendor.TenantID, vendor.Country, vendor.Subject}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func assertSelfSettlementDenied(t *testing.T, service *PostgresService, actor Actor, accountID, entryID, key string) {
	t.Helper()
	// The payout API validates the request key before it reaches live authority.
	// Keep every negative case long enough to exercise the source-session gate.
	key += "-self-settlement-001"
	if _, err := service.Ledger(actor, accountID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("%s ledger escaped source/party authority: %v", key, err)
	}
	if _, err := service.Payouts(actor, accountID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("%s payouts escaped source/party authority: %v", key, err)
	}
	if _, replay, err := service.RequestPayout(actor, key, []string{entryID}); !errors.Is(err, ErrForbidden) || replay {
		t.Fatalf("%s payout request/replay escaped source/party authority: replay=%t err=%v", key, replay, err)
	}
}

func verifySelfSettlementHTTPDenial(t *testing.T, service *PostgresService, rider Actor, entryID string) {
	t.Helper()
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/v1/settlements/ledger", "/v1/payouts"} {
		request := httptest.NewRequest(http.MethodGet, target+"?account_id="+rider.Subject, nil)
		request.Header.Set("X-Planext4u-Tenant", rider.TenantID)
		request.Header.Set("X-Planext4u-Country", rider.Country)
		request.Header.Set("X-Planext4u-Subject", rider.Subject)
		request.Header.Set("X-Planext4u-Roles", "RIDER")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), entryID) {
			t.Fatalf("direct HTTP self settlement without source %s: %d %s", target, response.Code, response.Body.String())
		}
	}
}

func verifyQueuedSelfSettlementRevocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service *PostgresService, vendor Actor, entryID string) {
	t.Helper()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM identity.sessions WHERE id=$1 FOR UPDATE`, vendor.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := service.Ledger(vendor, vendor.Subject)
		result <- e
	}()
	waiting := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND position('lock_participant_session' in query)>0)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("self ledger read did not queue behind source-session row lock")
	}
	if _, err = blocker.Exec(ctx, `UPDATE identity.sessions SET revoked_at=clock_timestamp() WHERE id=$1`, vendor.SourceSessionID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrForbidden) {
		t.Fatalf("queued self ledger read survived revocation: %v", err)
	}
	if _, replay, err := service.RequestPayout(vendor, "vendor-payout-request-self-001", []string{entryID}); !errors.Is(err, ErrForbidden) || replay {
		t.Fatalf("revoked payout response replay: replay=%t err=%v", replay, err)
	}
}
