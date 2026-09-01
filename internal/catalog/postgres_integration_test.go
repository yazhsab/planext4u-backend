//go:build integration

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCatalogProjectionIsolationAndQuestionControls(t *testing.T) {
	databaseURL := os.Getenv("CATALOG_DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("CATALOG_DATABASE_TEST_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS catalog CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `DROP SCHEMA IF EXISTS catalog CASCADE`)
	})
	for _, path := range []string{
		"../../migrations/platform/000001_service_roles.up.sql",
		"../../migrations/catalog/000001_catalog.up.sql",
		"../../migrations/catalog/000002_catalog_questions.up.sql",
		"../../migrations/catalog/000003_serviceability_zones.up.sql",
	} {
		migration, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, applyErr := pool.Exec(ctx, string(migration)); applyErr != nil {
			t.Fatalf("apply %s: %v", path, applyErr)
		}
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	tenantID := "d1f47ba2-1ad1-46bf-aa23-2969a9ea656f"
	category := Category{ID: "13765431-cf41-4a91-a6c8-8f09f33b4554", Name: "Daily needs", Priority: 10}
	item := Item{
		ID: categoryTestItemID, CategoryID: category.ID, Name: "Fresh milk", Summary: "One litre", Price: Money{AmountMinor: 6500, Currency: "INR"}, Available: true,
		SellerName: "Local Dairy", VerifiedLocalSeller: true, Variants: []Variant{{ID: "variant-milk-1l", Label: "1 litre", Price: Money{AmountMinor: 6500, Currency: "INR"}, Available: true, StockQuantity: 20, MaxPerOrder: 5}},
	}
	insertCatalogDocument(t, ctx, pool, tenantID, "IN", category.ID, "CATEGORY", category.Priority, category)
	insertCatalogDocument(t, ctx, pool, tenantID, "IN", item.ID, "ITEM", 10, item)
	if _, err := pool.Exec(ctx, `
		INSERT INTO catalog.serviceability_zones
			(tenant_id, country, id, locality, minimum_latitude, maximum_latitude,
			 minimum_longitude, maximum_longitude, postal_codes, revision, updated_at)
		VALUES ($1, 'IN', 'chennai-core', 'Chennai', 12.75, 13.35, 79.90, 80.50, ARRAY['600001'], 1, now())`, tenantID); err != nil {
		t.Fatal(err)
	}
	zones, err := repository.Zones(ctx)
	if err != nil || len(zones) != 1 || zones[0].TenantID != tenantID || zones[0].ID != "chennai-core" {
		t.Fatalf("zones=%#v err=%v", zones, err)
	}
	categories, err := repository.Categories(ctx, tenantID, "IN")
	if err != nil || len(categories) != 1 || categories[0].ID != category.ID {
		t.Fatalf("categories=%#v err=%v", categories, err)
	}
	items, err := repository.Items(ctx, tenantID, "IN")
	if err != nil || len(items) != 1 || items[0].ID != item.ID || len(items[0].Questions) != 0 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	other, err := repository.Items(ctx, "23bf7434-3643-49df-9928-c9169011f69d", "IN")
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-tenant items=%#v err=%v", other, err)
	}
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	first := Question{ID: "question-postgres-001", Question: "Is this delivered chilled?", AskedBy: "Planext4u customer", AskedByID: "customer-postgres-001", AskedAt: now}
	created, err := repository.AddQuestion(ctx, tenantID, "IN", item.ID, first)
	if err != nil || created.ID != first.ID {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replay, err := repository.AddQuestion(ctx, tenantID, "IN", item.ID, first)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	conflict := first
	conflict.Question = "A different question text"
	if _, err := repository.AddQuestion(ctx, tenantID, "IN", item.ID, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	for index := 2; index <= 5; index++ {
		question := Question{ID: "question-postgres-00" + string(rune('0'+index)), Question: "Pending question number " + string(rune('0'+index)), AskedBy: "Planext4u customer", AskedByID: first.AskedByID, AskedAt: now.Add(time.Duration(index) * time.Second)}
		if _, err := repository.AddQuestion(ctx, tenantID, "IN", item.ID, question); err != nil {
			t.Fatalf("add pending question %d: %v", index, err)
		}
	}
	sixth := Question{ID: "question-postgres-006", Question: "This pending question must be rejected", AskedBy: "Planext4u customer", AskedByID: first.AskedByID, AskedAt: now.Add(6 * time.Second)}
	if _, err := repository.AddQuestion(ctx, tenantID, "IN", item.ID, sixth); !errors.Is(err, ErrQuestionLimit) {
		t.Fatalf("question limit error=%v", err)
	}
	items, err = repository.Items(ctx, tenantID, "IN")
	if err != nil || len(items) != 1 || len(items[0].Questions) != 5 {
		t.Fatalf("items after questions=%#v err=%v", items, err)
	}
}

const categoryTestItemID = "af85100e-e09a-4f36-849b-1e1b7060ab53"

func insertCatalogDocument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, country, id, kind string, priority int, value any) {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO catalog.items
			(id, tenant_id, country, kind, title, status, priority, version, published_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, 'PUBLISHED', $6, 1, now(), now())`, id, tenantID, country, kind, string(document), priority); err != nil {
		t.Fatal(err)
	}
}
