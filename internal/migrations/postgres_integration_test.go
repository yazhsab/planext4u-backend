//go:build integration

package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBEMigrate001PostgresFromZeroUpgradeIsolationAndRollback(t *testing.T) {
	databaseURL := os.Getenv("MIGRATION_DATABASE_TEST_URL")
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || (parsed.Path != "/planext4u_local" && parsed.Path != "/planext4u_test") {
		t.Skip("MIGRATION_DATABASE_TEST_URL must target an explicitly allowed disposable local database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	migrations, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := NewRunner(connection)
	_ = runner.Down(ctx, migrations, "all", len(migrations))
	_, _ = connection.Exec(ctx, `DROP SCHEMA IF EXISTS platform_migrations CASCADE`)
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = runner.Down(cleanup, migrations, "all", len(migrations))
		_, _ = connection.Exec(cleanup, `DROP SCHEMA IF EXISTS platform_migrations CASCADE`)
	})
	if err := runner.Up(ctx, migrations, "all"); err != nil {
		t.Fatalf("from-zero migration: %v", err)
	}
	if err := runner.Up(ctx, migrations, "all"); err != nil {
		t.Fatalf("idempotent upgrade: %v", err)
	}
	for _, table := range []string{"identity.identities", "configuration.snapshots", "customer_web.sessions", "catalog.items", "media.assets", "audit.events", "messaging.outbox", "notification.deliveries", "support.tickets"} {
		var exists bool
		if err := connection.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s exists = %v, %v", table, exists, err)
		}
	}
	var catalogAccess, configurationAccess bool
	if err := connection.QueryRow(ctx, `SELECT has_schema_privilege('planext4u_catalog_runtime', 'catalog', 'USAGE'), has_schema_privilege('planext4u_catalog_runtime', 'configuration', 'USAGE')`).Scan(&catalogAccess, &configurationAccess); err != nil {
		t.Fatal(err)
	}
	if !catalogAccess || configurationAccess {
		t.Fatalf("catalog runtime schema access = catalog:%v configuration:%v", catalogAccess, configurationAccess)
	}

	failingSQL := "CREATE TABLE catalog.failed_upgrade(id bigint); SELECT 1 / 0;"
	digest := sha256.Sum256([]byte(failingSQL))
	var failingVersion int64 = 1
	for _, migration := range migrations {
		if migration.Service == "catalog" && migration.Version >= failingVersion {
			failingVersion = migration.Version + 1
		}
	}
	failing := append(append([]Migration(nil), migrations...), Migration{Service: "catalog", Version: failingVersion, Name: "failed_upgrade", Up: failingSQL, Down: "DROP TABLE IF EXISTS catalog.failed_upgrade", Checksum: hex.EncodeToString(digest[:])})
	if err := runner.Up(ctx, failing, "catalog"); err == nil {
		t.Fatal("failed migration unexpectedly succeeded")
	}
	var relationExists, ledgerExists bool
	if err := connection.QueryRow(ctx, `SELECT to_regclass('catalog.failed_upgrade') IS NOT NULL`).Scan(&relationExists); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform_migrations.applied WHERE service = 'catalog' AND version = $1)`, failingVersion).Scan(&ledgerExists); err != nil {
		t.Fatal(err)
	}
	if relationExists || ledgerExists {
		t.Fatalf("failed migration leaked state: relation=%v ledger=%v", relationExists, ledgerExists)
	}

	modified := append([]Migration(nil), migrations...)
	for index := range modified {
		if modified[index].Service == "catalog" {
			modified[index].Checksum = strings.Repeat("f", 64)
			break
		}
	}
	if err := runner.Up(ctx, modified, "catalog"); err == nil || !strings.Contains(err.Error(), "checksum drift") {
		t.Fatalf("checksum drift error = %v", err)
	}
}
