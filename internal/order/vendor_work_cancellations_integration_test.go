//go:build integration

package order

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVendorWorkCancellationOutboxBeforeAndAfterPlacement(t *testing.T) {
	databaseURL := os.Getenv("ORDER_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("ORDER_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS ordering CASCADE`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/ordering/000001_ordering.up.sql",
		"../../migrations/ordering/000002_durable_lifecycle.up.sql",
		"../../migrations/ordering/000003_vendor_work_intents.up.sql",
		"../../migrations/ordering/000004_vendor_work_cancellations.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	service, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil || service.Ready(ctx) != nil {
		t.Fatalf("order service=%#v err=%v", service, err)
	}
	scope := Scope{TenantID: postgresOrderTenantOne, Country: "IN", CustomerID: postgresOrderCustomerOne}
	pendingSnapshot := orderSnapshot()
	pendingSnapshot.PaymentID = "payment-cancel-before-placement"
	pending, replay, err := service.Create(scope, "order-cancel-pending-0001", pendingSnapshot, false)
	if err != nil || replay || pending.Status != StatusPendingPayment {
		t.Fatalf("pending order=%#v replay=%t err=%v", pending, replay, err)
	}
	requested, replay, err := service.Transition(scope, "order-cancel-request-0001", pending.ID, 1, StatusCancelRequested, "CUSTOMER", "Changed mind")
	if err != nil || replay || requested.Revision != 2 {
		t.Fatalf("pre-placement request=%#v replay=%t err=%v", requested, replay, err)
	}
	if _, replay, err := service.Transition(scope, "order-cancel-request-0001", pending.ID, 1, StatusCancelRequested, "CUSTOMER", "Changed mind"); err != nil || !replay {
		t.Fatalf("request replay=%t err=%v", replay, err)
	}
	var workCount, cancelCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_intents WHERE order_id=$1`, pending.ID).Scan(&workCount); err != nil || workCount != 0 {
		t.Fatalf("pre-placement work intents=%d err=%v", workCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_cancellation_intents WHERE order_id=$1`, pending.ID).Scan(&cancelCount); err != nil || cancelCount != 1 {
		t.Fatalf("pre-placement cancellation intents=%d err=%v", cancelCount, err)
	}
	if _, _, err := service.Transition(scope, "order-capture-after-cancel", pending.ID, 2, StatusPlaced, "PLATFORM", "late payment"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("placement after cancellation request=%v", err)
	}
	final, replay, err := service.Transition(scope, "order-cancel-finalize-0001", pending.ID, 2, StatusCancelled, "PLATFORM", "Approved")
	if err != nil || replay || final.Revision != 3 {
		t.Fatalf("pre-placement final=%#v replay=%t err=%v", final, replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_cancellation_intents WHERE order_id=$1`, pending.ID).Scan(&cancelCount); err != nil || cancelCount != 2 {
		t.Fatalf("final cancellation intents=%d err=%v", cancelCount, err)
	}
	placedSnapshot := orderSnapshot()
	placedSnapshot.PaymentID = "payment-cancel-after-placement"
	placed, _, err := service.Create(scope, "order-place-before-cancel-1", placedSnapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Transition(scope, "order-cancel-placed-0001", placed.ID, 1, StatusCancelRequested, "CUSTOMER", "Changed mind")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ordering.vendor_work_intents WHERE order_id=$1`, placed.ID).Scan(&workCount); err != nil || workCount != 1 {
		t.Fatalf("placed work intents=%d err=%v", workCount, err)
	}
	claims, err := service.ClaimVendorWorkCancellations(ctx, 10)
	if err != nil || len(claims) != 3 {
		t.Fatalf("cancellation claims=%#v err=%v", claims, err)
	}
	for _, claim := range claims {
		if claim.SourceWorkIntentID != vendorWorkSourceID(claim.OrderID, claim.VendorID) ||
			claim.ID != vendorWorkCancellationID(claim.OrderID, claim.VendorID, claim.OrderRevision, claim.OrderStatus) ||
			claim.TenantID != scope.TenantID || claim.Country != scope.Country || claim.CustomerID != scope.CustomerID {
			t.Fatalf("claim authority=%#v", claim)
		}
		if err := service.AckVendorWorkCancellation(ctx, claim.ID, claim.ClaimToken); err != nil {
			t.Fatal(err)
		}
		if err := service.AckVendorWorkCancellation(ctx, claim.ID, claim.ClaimToken); !errors.Is(err, ErrVendorWorkCancellationClaimLost) {
			t.Fatalf("duplicate ack=%v", err)
		}
	}
	if remaining, err := service.ClaimVendorWorkCancellations(ctx, 10); err != nil || len(remaining) != 0 {
		t.Fatalf("remaining cancellation claims=%#v err=%v", remaining, err)
	}
}
