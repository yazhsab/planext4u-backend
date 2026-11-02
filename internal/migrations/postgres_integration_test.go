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
	if databaseURL == "" {
		t.Skip("MIGRATION_DATABASE_TEST_URL is not set")
	}
	if !dedicatedMigrationDatabase(databaseURL) {
		t.Fatal("migration tests require the dedicated unexposed disposable migration instance; application/shared clusters are forbidden")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	migrations, err := Discover("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := NewRunner(connection)
	var existing int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname=ANY($1)`, serviceOrder).Scan(&existing); err != nil || existing != 0 {
		t.Fatal("migration verification instance is not from zero")
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = runner.Down(cleanup, migrations, "all", len(migrations))
		_, _ = connection.Exec(cleanup, `DROP SCHEMA IF EXISTS platform_migrations CASCADE`)
	})
	if err := runner.Up(ctx, migrations, "all"); err != nil {
		t.Fatalf("from-zero migration: %v", err)
	}
	var applied int
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM platform_migrations.applied`).Scan(&applied); err != nil || applied != len(migrations) {
		t.Fatalf("applied migration count=%d, want %d: %v", applied, len(migrations), err)
	}
	t.Logf("Applied all %d discovered migrations on a fresh dedicated instance", applied)
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
	if _, err := connection.Exec(ctx, `INSERT INTO supply.privacy_erasure_runs
	 (request_id,tenant_id,subject_id,policy_version,countries,application_rows,document_rows,submission_rows,occurred_at)
	 VALUES('synthetic-rollback-guard','tenant-synthetic','subject-synthetic','policy',ARRAY['IN'],0,0,0,clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if err := runner.Down(ctx, migrations, "all", len(migrations)); err == nil || !strings.Contains(err.Error(), "privacy evidence") {
		t.Fatalf("complete rollback accepted retained privacy evidence: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM platform_migrations.applied`).Scan(&applied); err != nil || applied != len(migrations) {
		t.Fatalf("privacy rollback preflight changed %d applied migrations: %v", applied, err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM supply.privacy_erasure_runs WHERE request_id='synthetic-rollback-guard'`); err != nil {
		t.Fatal(err)
	}
	if err := runner.Down(ctx, migrations, "identity", 1); err == nil || !strings.Contains(err.Error(), "dependent owner services") {
		t.Fatalf("identity rollback ignored installed dependent services: %v", err)
	}
	if err := runner.Down(ctx, migrations, "all", len(migrations)); err != nil {
		t.Fatalf("complete from-zero rollback: %v", err)
	}
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname=ANY($1)`, serviceOrder).Scan(&existing); err != nil || existing != 0 {
		t.Fatalf("rollback left %d owned schemas: %v", existing, err)
	}
	if err := connection.QueryRow(ctx, `SELECT count(*) FROM platform_migrations.applied`).Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("rollback left %d ledger records: %v", applied, err)
	}
}

func dedicatedMigrationDatabase(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() != "migration-postgres" || parsed.Path != "/planext4u_migration_verification" || parsed.RawQuery != "sslmode=disable" {
		return false
	}
	config, err := pgx.ParseConfig(raw)
	if err != nil || config.Host != "migration-postgres" || config.Port != 5432 || config.Database != "planext4u_migration_verification" || config.User != "planext4u_verify" {
		return false
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			return false
		}
	}
	return true
}

func TestMigrationVerificationRefusesApplicationAndConnectionOverrides(t *testing.T) {
	for _, raw := range []string{
		"postgres://planext4u_local:synthetic@127.0.0.1:54320/planext4u_local?sslmode=disable",
		"postgres://planext4u_verify:synthetic@postgres:5432/planext4u_phase0_verification?sslmode=disable",
		"postgres://planext4u_verify:synthetic@migration-postgres:5432/planext4u_migration_verification?host=production.example",
		"postgres://planext4u_verify:synthetic@migration-postgres:5432/planext4u_migration_verification?dbname=planext4u_local",
	} {
		if dedicatedMigrationDatabase(raw) {
			t.Fatal("unsafe migration target accepted")
		}
	}
	if !dedicatedMigrationDatabase("postgres://planext4u_verify:synthetic@migration-postgres:5432/planext4u_migration_verification?sslmode=disable") {
		t.Fatal("dedicated fixture target rejected")
	}
}
