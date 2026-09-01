//go:build integration

package inventory

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresInventoryPreventsOversellAndPersistsLifecycle(t *testing.T) {
	databaseURL := os.Getenv("INVENTORY_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("INVENTORY_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS inventory CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS inventory CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000003_transaction_roles.up.sql",
		"../../migrations/inventory/000001_inventory.up.sql",
		"../../migrations/inventory/000002_restock_idempotency.up.sql",
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
	service, err := NewPostgresService(pool, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: inventoryTenantOne, Country: "IN"}
	for _, stock := range []struct {
		variant  string
		quantity int
	}{{inventoryVariantOne, 5}, {inventoryVariantTwo, 3}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO inventory.stock (tenant_id,country,variant_id,available_quantity,revision,updated_at)
			VALUES ($1,$2,$3,$4,1,$5)`, scope.TenantID, scope.Country, stock.variant, stock.quantity, now); err != nil {
			t.Fatal(err)
		}
	}

	results := make(chan struct {
		value Reservation
		err   error
	}, 2)
	var wait sync.WaitGroup
	for _, key := range []string{"inventory-reserve-000001", "inventory-reserve-000002"} {
		wait.Add(1)
		go func(idempotencyKey string) {
			defer wait.Done()
			value, _, callErr := service.Reserve(scope, idempotencyKey, idempotencyKey, []Line{{VariantID: inventoryVariantOne, Quantity: 4}}, now.Add(10*time.Minute))
			results <- struct {
				value Reservation
				err   error
			}{value, callErr}
		}(key)
	}
	wait.Wait()
	close(results)
	var reserved Reservation
	succeeded, rejected := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
			reserved = result.value
		case errors.Is(result.err, ErrInsufficientStock):
			rejected++
		default:
			t.Fatalf("unexpected concurrent reservation error: %v", result.err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("oversell results succeeded=%d rejected=%d", succeeded, rejected)
	}
	if available, err := service.Available(scope, inventoryVariantOne); err != nil || available != 1 {
		t.Fatalf("available after reservation=%d err=%v", available, err)
	}
	committed, err := service.Commit(scope, reserved.ID)
	if err != nil || committed.State != StateCommitted {
		t.Fatalf("committed=%#v err=%v", committed, err)
	}
	restocked, replay, err := service.Restock(scope, "inventory-restock-000001", reserved.ID, []Line{{VariantID: inventoryVariantOne, Quantity: 2}})
	if err != nil || replay || len(restocked.RestockedLines) != 1 || restocked.RestockedLines[0].Quantity != 2 {
		t.Fatalf("restocked=%#v replay=%t err=%v", restocked, replay, err)
	}
	restarted, _ := NewPostgresService(pool, func() time.Time { return now })
	replayed, replay, err := restarted.Restock(scope, "inventory-restock-000001", reserved.ID, []Line{{VariantID: inventoryVariantOne, Quantity: 2}})
	if err != nil || !replay || replayed.UpdatedAt != restocked.UpdatedAt {
		t.Fatalf("restock replay=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.Restock(scope, "inventory-restock-000001", reserved.ID, []Line{{VariantID: inventoryVariantOne, Quantity: 1}}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("restock conflict=%v", err)
	}
	if available, err := restarted.Available(scope, inventoryVariantOne); err != nil || available != 3 {
		t.Fatalf("available after restock=%d err=%v", available, err)
	}

	expiring, replay, err := restarted.Reserve(scope, "inventory-expire-0000001", "order-expire-00000001", []Line{{VariantID: inventoryVariantTwo, Quantity: 2}}, now.Add(time.Minute))
	if err != nil || replay {
		t.Fatalf("expiring=%#v replay=%t err=%v", expiring, replay, err)
	}
	now = now.Add(2 * time.Minute)
	if expired := restarted.Expire(); expired != 1 {
		t.Fatalf("expired=%d", expired)
	}
	loaded, err := restarted.Get(scope, expiring.ID)
	if err != nil || loaded.State != StateReleased {
		t.Fatalf("expired reservation=%#v err=%v", loaded, err)
	}
	if available, err := restarted.Available(scope, inventoryVariantTwo); err != nil || available != 3 {
		t.Fatalf("available after expiry=%d err=%v", available, err)
	}
	other, err := restarted.Get(Scope{TenantID: inventoryTenantTwo, Country: "IN"}, reserved.ID)
	if !errors.Is(err, ErrReservationNotFound) || other.ID != "" {
		t.Fatalf("cross tenant reservation=%#v err=%v", other, err)
	}
}

const (
	inventoryTenantOne  = "ce2774ae-a442-4510-8ed0-0532e70aa2a9"
	inventoryTenantTwo  = "8f88bead-d515-40f9-a3e5-5ab256841f62"
	inventoryVariantOne = "068588d7-ae07-4917-ac00-725533092fb0"
	inventoryVariantTwo = "7ba57c45-a6d2-41fa-a9ca-5357f6c83c58"
)
