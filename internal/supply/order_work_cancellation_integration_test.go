//go:build integration

package supply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/order"
)

func TestOrderWorkCancellationTombstoneReplayAndVendorRace(t *testing.T) {
	databaseURL := os.Getenv("SUPPLY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("SUPPLY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, `DROP SCHEMA IF EXISTS supply CASCADE`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000002_commerce_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/platform/000005_phase4_roles.up.sql",
		"../../migrations/platform/000007_transaction_runtime.up.sql",
		"../../migrations/platform/000010_transaction_supply_runtime.up.sql",
		"../../migrations/supply/000001_supply.up.sql",
		"../../migrations/supply/000002_durable_vendor_runtime.up.sql",
		"../../migrations/supply/000010_order_work_ingestions.up.sql",
		"../../migrations/supply/000011_order_work_cancellations.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := owner.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	const (
		tenant   = "559d37a4-2458-43f8-886d-89d33aa8e482"
		other    = "009523a1-4c7d-4530-a2d0-ece121f75b37"
		vendor   = "60000000-0000-4000-8000-000000000101"
		customer = "78b2e2e4-bc18-45af-89f9-b139aceab1e4"
	)
	if _, err := owner.Exec(ctx, `
		INSERT INTO supply.vendor_applications
			(id,tenant_id,country,vendor_identity_id,revision,status,business_name,
			 business_type,contact_name,verified,created_at,updated_at)
		VALUES ($1,$2,'IN',$3,1,'APPROVED','Fixture vendor','SOLE_PROPRIETOR',
			'Synthetic owner',true,clock_timestamp(),clock_timestamp())`,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte(vendor)).String(), tenant, vendor); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, `SET ROLE planext4u_transaction_runtime`)
		return err
	}
	runtimePool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	service, err := NewPostgresService(runtimePool, func() time.Time { return now })
	if err != nil || service.OrderWorkCancellationReady(ctx) != nil {
		t.Fatalf("cancellation service=%#v err=%v", service, err)
	}
	preOrderID := "30000000-0000-4000-8000-000000000111"
	preWork := orderWorkFixture(tenant, customer, preOrderID, vendor)
	preCancel := orderWorkCancellationFixture(tenant, customer, preOrderID, vendor, 2)
	preResult, err := service.IngestOrderWorkCancellation(ctx, preCancel)
	if err != nil || preResult.Outcome != "NO_WORK" || preResult.WorkID != "" || preResult.Replayed {
		t.Fatalf("pre-placement cancellation=%#v err=%v", preResult, err)
	}
	preReplay, err := service.IngestOrderWorkCancellation(ctx, preCancel)
	if err != nil || !preReplay.Replayed || preReplay.Outcome != "NO_WORK" {
		t.Fatalf("pre-placement replay=%#v err=%v", preReplay, err)
	}
	changed := preCancel
	changed.TenantID = other
	if _, err := service.IngestOrderWorkCancellation(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-tenant cancellation replay=%v", err)
	}
	changed = preCancel
	changed.OrderRevision = 3
	if _, err := service.IngestOrderWorkCancellation(ctx, changed); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("non-deterministic cancellation id=%v", err)
	}
	if _, _, err := service.IngestOrderWork(ctx, preWork); !errors.Is(err, ErrOrderWorkCancelled) {
		t.Fatalf("late work after tombstone=%v", err)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.work_items WHERE reference_id=$1`, preOrderID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pre-cancelled work rows=%d err=%v", count, err)
	}
	postOrderID := "30000000-0000-4000-8000-000000000112"
	postWork := orderWorkFixture(tenant, customer, postOrderID, vendor)
	work, _, err := service.IngestOrderWork(ctx, postWork)
	if err != nil {
		t.Fatal(err)
	}
	postCancel := orderWorkCancellationFixture(tenant, customer, postOrderID, vendor, 2)
	postResult, err := service.IngestOrderWorkCancellation(ctx, postCancel)
	if err != nil || postResult.Outcome != "CANCELLED" || postResult.WorkID != work.ID {
		t.Fatalf("post-placement cancellation=%#v err=%v", postResult, err)
	}
	postReplay, err := service.IngestOrderWorkCancellation(ctx, postCancel)
	if err != nil || !postReplay.Replayed || postReplay.Outcome != "CANCELLED" || postReplay.WorkID != work.ID {
		t.Fatalf("post-placement replay=%#v err=%v", postReplay, err)
	}
	vendorActor := Actor{TenantID: tenant, Country: "IN", Subject: vendor, Roles: []string{"VENDOR"}}
	if _, _, err := service.TransitionWork(vendorActor, "vendor-after-cancel-0001", work.ID, "ACCEPTED"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("vendor accepted cancelled work=%v", err)
	}
	finalCancel := orderWorkCancellationFixture(tenant, customer, postOrderID, vendor, 3)
	finalCancel.OrderStatus = order.StatusCancelled
	finalCancel.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("order-vendor-cancel:%s:%s:%d:%s", postOrderID, vendor, 3, finalCancel.OrderStatus))).String()
	finalResult, err := service.IngestOrderWorkCancellation(ctx, finalCancel)
	if err != nil || finalResult.Outcome != "ALREADY_CANCELLED" {
		t.Fatalf("final cancellation=%#v err=%v", finalResult, err)
	}
	raceOrderID := "30000000-0000-4000-8000-000000000113"
	raceWork, _, err := service.IngestOrderWork(ctx, orderWorkFixture(tenant, customer, raceOrderID, vendor))
	if err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"ACCEPTED", "PREPARING", "READY"} {
		if _, _, err := service.TransitionWork(vendorActor, fmt.Sprintf("vendor-race-prepare-%04d", index), raceWork.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	raceCancel := orderWorkCancellationFixture(tenant, customer, raceOrderID, vendor, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	var raceOutcome CancellationOutcome
	var cancelErr, completeErr error
	go func() {
		defer wait.Done()
		raceOutcome, cancelErr = service.IngestOrderWorkCancellation(ctx, raceCancel)
	}()
	go func() {
		defer wait.Done()
		_, _, completeErr = service.TransitionWork(vendorActor, "vendor-race-complete-01", raceWork.ID, "COMPLETED")
	}()
	wait.Wait()
	if cancelErr != nil {
		t.Fatalf("concurrent cancellation=%v", cancelErr)
	}
	var finalStatus string
	if err := owner.QueryRow(ctx, `SELECT status FROM supply.work_items WHERE id=$1`, raceWork.ID).Scan(&finalStatus); err != nil {
		t.Fatal(err)
	}
	switch raceOutcome.Outcome {
	case "CANCELLED":
		if finalStatus != "CANCELLED" || !errors.Is(completeErr, ErrInvalidTransition) {
			t.Fatalf("cancel-won race status=%s completion=%v", finalStatus, completeErr)
		}
	case "ALREADY_COMPLETED":
		if finalStatus != "COMPLETED" || completeErr != nil {
			t.Fatalf("completion-won race status=%s completion=%v", finalStatus, completeErr)
		}
	default:
		t.Fatalf("untracked cancellation race=%#v", raceOutcome)
	}
	completedOrderID := "30000000-0000-4000-8000-000000000114"
	completedWork, _, err := service.IngestOrderWork(ctx, orderWorkFixture(tenant, customer, completedOrderID, vendor))
	if err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"ACCEPTED", "PREPARING", "READY", "COMPLETED"} {
		if _, _, err := service.TransitionWork(vendorActor, fmt.Sprintf("vendor-finished-before-cancel-%04d", index), completedWork.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	completedCancel := orderWorkCancellationFixture(tenant, customer, completedOrderID, vendor, 2)
	completedOutcome, err := service.IngestOrderWorkCancellation(ctx, completedCancel)
	if err != nil || completedOutcome.Outcome != "ALREADY_COMPLETED" || completedOutcome.WorkID != completedWork.ID {
		t.Fatalf("completed work reconciliation=%#v err=%v", completedOutcome, err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.order_work_cancellations WHERE source_cancellation_id=$1 AND outcome='ALREADY_COMPLETED'`, completedCancel.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("completed work discrepancy receipt=%d err=%v", count, err)
	}
	if _, err := runtimePool.Exec(ctx, `INSERT INTO supply.order_work_cancellations(source_cancellation_id) VALUES ($1)`, uuid.NewString()); err == nil {
		t.Fatal("transaction runtime inserted cancellation ledger directly")
	} else {
		var database *pgconn.PgError
		if !errors.As(err, &database) || database.Code != "42501" {
			t.Fatalf("direct cancellation ledger insert=%v", err)
		}
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.order_work_cancellations`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("cancellation ledger rows=%d err=%v", count, err)
	}
}

func orderWorkCancellationFixture(tenant, customer, orderID, vendor string, revision int64) OrderWorkCancellation {
	status := order.StatusCancelRequested
	return OrderWorkCancellation{
		ID:                 uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("order-vendor-cancel:%s:%s:%d:%s", orderID, vendor, revision, status))).String(),
		SourceWorkIntentID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+orderID+":"+vendor)).String(),
		TenantID:           tenant, Country: "IN", CustomerID: customer,
		OrderID: orderID, VendorID: vendor, OrderRevision: revision, OrderStatus: status,
	}
}
