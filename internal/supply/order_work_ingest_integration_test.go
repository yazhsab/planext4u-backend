//go:build integration

package supply

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yazhsab/planext4u-backend/internal/order"
)

func TestOrderWorkIngressOwnerApprovalReplayAndIsolation(t *testing.T) {
	databaseURL := os.Getenv("SUPPLY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("SUPPLY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(ctx, `DROP SCHEMA IF EXISTS supply CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = owner.Exec(cleanup, `DROP SCHEMA IF EXISTS supply CASCADE`)
	})
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
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := owner.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	const (
		tenantA  = "559d37a4-2458-43f8-886d-89d33aa8e482"
		tenantB  = "009523a1-4c7d-4530-a2d0-ece121f75b37"
		vendorA  = "60000000-0000-4000-8000-000000000101"
		vendorB  = "60000000-0000-4000-8000-000000000102"
		inactive = "60000000-0000-4000-8000-000000000103"
		customer = "78b2e2e4-bc18-45af-89f9-b139aceab1e4"
		orderID  = "30000000-0000-4000-8000-000000000101"
	)
	for index, vendorID := range []string{vendorA, vendorB, inactive} {
		status, verified := "APPROVED", true
		if vendorID == inactive {
			status, verified = "REGISTERED", false
		}
		if _, err := owner.Exec(ctx, `
			INSERT INTO supply.vendor_applications
				(id,tenant_id,country,vendor_identity_id,revision,status,business_name,
				 business_type,contact_name,verified,created_at,updated_at)
			VALUES ($1,$2,'IN',$3,1,$4,'Fixture vendor','SOLE_PROPRIETOR',
				'Synthetic owner',$5,clock_timestamp(),clock_timestamp())`,
			uuid.NewSHA1(uuid.NameSpaceOID, []byte(vendorID)).String(), tenantA,
			vendorID, status, verified); err != nil {
			t.Fatalf("seed vendor %d: %v", index, err)
		}
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
	if err != nil || service.OrderWorkIngressReady(ctx) != nil {
		t.Fatalf("ingress service=%#v err=%v", service, err)
	}
	first := orderWorkFixture(tenantA, customer, orderID, vendorA)
	value, replay, err := service.IngestOrderWork(ctx, first)
	if err != nil || replay || value.Status != "NEW" || value.ReferenceType != "MARKETPLACE_ORDER" || value.ReferenceID != orderID || value.Total.AmountMinor != 19800 {
		t.Fatalf("first vendor work=%#v replay=%t err=%v", value, replay, err)
	}
	restarted, _ := NewPostgresService(runtimePool, func() time.Time { return now })
	again, replay, err := restarted.IngestOrderWork(ctx, first)
	if err != nil || !replay || again.ID != value.ID {
		t.Fatalf("replayed vendor work=%#v replay=%t err=%v", again, replay, err)
	}
	changed := first
	changed.Lines = append([]order.LineSnapshot(nil), first.Lines...)
	changed.Lines[0].ItemName = "Changed after placement"
	if _, _, err := restarted.IngestOrderWork(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed source replay=%v", err)
	}
	crossTenant := first
	crossTenant.TenantID = tenantB
	if _, _, err := restarted.IngestOrderWork(ctx, crossTenant); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-tenant source replay=%v", err)
	}
	second := orderWorkFixture(tenantA, customer, orderID, vendorB)
	secondWork, replay, err := restarted.IngestOrderWork(ctx, second)
	if err != nil || replay || secondWork.ID == value.ID {
		t.Fatalf("second vendor work=%#v replay=%t err=%v", secondWork, replay, err)
	}
	bad := orderWorkFixture(tenantA, customer, "30000000-0000-4000-8000-000000000102", inactive)
	if _, _, err := restarted.IngestOrderWork(ctx, bad); !errors.Is(err, ErrOrderWorkVendorInactive) {
		t.Fatalf("inactive vendor work=%v", err)
	}
	bad = orderWorkFixture(tenantA, customer, "30000000-0000-4000-8000-000000000103", "60000000-0000-4000-8000-000000000104")
	if _, _, err := restarted.IngestOrderWork(ctx, bad); !errors.Is(err, ErrOrderWorkVendorInactive) {
		t.Fatalf("unknown vendor work=%v", err)
	}
	bad = first
	bad.IntentID = uuid.NewString()
	if _, _, err := restarted.IngestOrderWork(ctx, bad); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("forged source identity=%v", err)
	}
	var workCount, ledgerCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.work_items WHERE reference_id=$1`, orderID).Scan(&workCount); err != nil || workCount != 2 {
		t.Fatalf("work rows=%d err=%v", workCount, err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM supply.order_work_ingestions WHERE order_id=$1`, orderID).Scan(&ledgerCount); err != nil || ledgerCount != 2 {
		t.Fatalf("ingestion rows=%d err=%v", ledgerCount, err)
	}
	if _, err := runtimePool.Exec(ctx, `INSERT INTO supply.order_work_ingestions(source_intent_id) VALUES ($1)`, uuid.NewString()); err == nil {
		t.Fatal("transaction runtime directly inserted the supply ingress ledger")
	} else {
		var database *pgconn.PgError
		if !errors.As(err, &database) || database.Code != "42501" {
			t.Fatalf("direct ledger insert error=%v", err)
		}
	}
}

func orderWorkFixture(tenant, customer, orderID, vendor string) OrderWorkIngress {
	line := order.LineSnapshot{
		VendorID: vendor, ItemID: "40000000-0000-4000-8000-000000000101",
		VariantID: "50000000-0000-4000-8000-000000000101", ItemName: "Local oil",
		Quantity: 2, UnitPrice: order.Money{AmountMinor: 10000, Currency: "INR"},
		LineTotal: order.Money{AmountMinor: 20000, Currency: "INR"}, TaxMinor: 800,
		DiscountMinor: 1000, CommissionMinor: 2400,
	}
	return OrderWorkIngress{
		IntentID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+orderID+":"+vendor)).String(),
		TenantID: tenant, Country: "IN", CustomerID: customer, OrderID: orderID,
		VendorID: vendor, OrderRevision: 1, Lines: []order.LineSnapshot{line},
		Currency: "INR", ProductTaxTreatment: "EXCLUSIVE",
	}
}
