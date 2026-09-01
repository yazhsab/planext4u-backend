//go:build integration

package commerce

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCartPersistenceIdempotencyAndConcurrency(t *testing.T) {
	databaseURL := os.Getenv("COMMERCE_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("COMMERCE_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS commerce CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DROP SCHEMA IF EXISTS commerce CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/platform/000002_commerce_roles.up.sql",
		"../../migrations/commerce/000001_commerce.up.sql",
		"../../migrations/commerce/000002_checkout_pricing.up.sql",
		"../../migrations/commerce/000003_customer_address_book.up.sql",
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
	provider := postgresCartProvider{values: map[string]VariantSnapshot{
		postgresVariantOne: {
			VariantID: postgresVariantOne, ItemID: postgresItemOne, VendorID: postgresVendorOne,
			ItemName: "Local rice", VariantName: "5 kg", UnitPrice: Money{AmountMinor: 75000, Currency: "INR"},
			Available: true, Stock: 20, MaxPerOrder: 5,
		},
		postgresVariantTwo: {
			VariantID: postgresVariantTwo, ItemID: postgresItemTwo, VendorID: postgresVendorOne,
			ItemName: "Groundnut oil", VariantName: "1 litre", UnitPrice: Money{AmountMinor: 21000, Currency: "INR"},
			Available: true, Stock: 20, MaxPerOrder: 5,
		},
	}}
	service, err := NewPostgresService(pool, provider, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: postgresTenantOne, Country: "IN", CustomerID: postgresCustomerOne}
	empty, err := service.Get(scope)
	if err != nil || empty.Revision != 0 || len(empty.Items) != 0 {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
	created, replay, err := service.Change(ctx, scope, "commerce-create-00000001", 0, postgresVariantOne, 2)
	if err != nil || replay || created.Revision != 1 || created.Total.AmountMinor != 150000 {
		t.Fatalf("created=%#v replay=%t err=%v", created, replay, err)
	}
	restarted, _ := NewPostgresService(pool, provider, func() time.Time { return now.Add(time.Second) })
	replayed, replay, err := restarted.Change(ctx, scope, "commerce-create-00000001", 0, postgresVariantOne, 2)
	if err != nil || !replay || replayed.ID != created.ID || replayed.Revision != 1 {
		t.Fatalf("replayed=%#v replay=%t err=%v", replayed, replay, err)
	}
	if _, _, err := restarted.Change(ctx, scope, "commerce-create-00000001", 0, postgresVariantOne, 3); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	if _, _, err := restarted.Change(ctx, scope, "commerce-stale-000000001", 0, postgresVariantTwo, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision conflict=%v", err)
	}

	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, request := range []struct {
		key     string
		variant string
	}{
		{"commerce-concurrent-0001", postgresVariantOne},
		{"commerce-concurrent-0002", postgresVariantTwo},
	} {
		wait.Add(1)
		go func(key, variant string) {
			defer wait.Done()
			_, _, callErr := restarted.Change(context.Background(), scope, key, 1, variant, 1)
			results <- callErr
		}(request.key, request.variant)
	}
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for callErr := range results {
		switch {
		case callErr == nil:
			succeeded++
		case errors.Is(callErr, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent result: %v", callErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent results succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	loaded, err := restarted.Get(scope)
	if err != nil || loaded.Revision != 2 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	other, err := restarted.Get(Scope{TenantID: postgresTenantTwo, Country: "IN", CustomerID: postgresCustomerOne})
	if err != nil || other.Revision != 0 || len(other.Items) != 0 || other.ID == loaded.ID {
		t.Fatalf("cross tenant=%#v err=%v", other, err)
	}
}

type postgresCartProvider struct{ values map[string]VariantSnapshot }

func (provider postgresCartProvider) Resolve(_ context.Context, _ Scope, variantID string) (VariantSnapshot, error) {
	value, found := provider.values[variantID]
	if !found {
		return VariantSnapshot{}, ErrVariantNotFound
	}
	return value, nil
}

const (
	postgresTenantOne   = "c688a212-50fc-4d5c-b370-35a8c1d04f55"
	postgresTenantTwo   = "d05cf731-e447-4c72-bafd-83088bb777f9"
	postgresCustomerOne = "28d69d2b-ad95-437b-b600-520691ceb6a8"
	postgresVendorOne   = "14ec2e38-84c3-4b3c-91d7-4c8df2ad61dd"
	postgresItemOne     = "dc3c9658-a66c-42c1-8f24-6db3cd913791"
	postgresItemTwo     = "7c90bf5e-a52a-415a-97d4-c50ae4dd9ca4"
	postgresVariantOne  = "6e2664cc-6824-45d5-9b49-293620a10038"
	postgresVariantTwo  = "2f446b0e-b400-40d6-acfa-1a210806f79d"
)
