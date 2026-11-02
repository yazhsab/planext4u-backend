#!/usr/bin/env sh
set -eu
cd /src
case "${1:-}" in
  manifest)
    find . -type f ! -path './coverage/*' -exec sha256sum {} + | sort > /evidence/source.sha256
    ;;
  quality)
    make verify
    ;;
  postgres)
    # This database exists only in this run's private Compose project. The
    # integration suites intentionally recreate schemas; never use the app DB.
    test_url='postgres://planext4u_verify:synthetic-verification-only@postgres:5432/planext4u_phase0_verification?sslmode=disable'
    for domain in IDENTITY CONFIGURATION CATALOG COMMERCE CHECKOUT INVENTORY ORDER PAYMENT BOOKING SUPPLY FOOD FULFILLMENT EMERGENCY LOCAL_VERTICALS SOCIAL GOVERNANCE WALLET AUDIT ADMIN CUSTOMER_WEB MEDIA MESSAGING NOTIFICATION MIGRATION SUPPORT BANK_VERIFICATION; do
      export "${domain}_DATABASE_TEST_URL=$test_url"
    done
    # Role creation/rollback is cluster-wide. Use a separate ephemeral instance
    # so other owning-service fixtures cannot hide or obstruct a from-zero run.
    export MIGRATION_DATABASE_TEST_URL='postgres://planext4u_verify:synthetic-verification-only@migration-postgres:5432/planext4u_migration_verification?sslmode=disable'
    export REDIS_TEST_URL=redis://redis:6379/15 NATS_TEST_URL=nats://nats:4222
    export MEDIA_S3_TEST_ENDPOINT=http://object-store:9000 MEDIA_S3_TEST_ACCESS_KEY=synthetic-verification MEDIA_S3_TEST_SECRET_KEY=synthetic-verification-object-only MEDIA_S3_TEST_BUCKET=verification-media
    # The due-version fixture drops schemas only inside this disposable project.
    # Opt it in here so the new safety/role/race cases cannot silently skip.
    export MEDIA_PURGE_TEST_DISPOSABLE=1
    : > /evidence/postgres-results.log
    for target in identity gateway configuration catalog commerce checkout inventory order payment booking supply food fulfillment emergency local-verticals social governance wallet audit admin customer-web media messaging notification migrations support bank-verification; do
      printf 'Running %s integration\n' "$target"
      make "test-$target-integration" > "/evidence/postgres-$target.log" 2>&1
      if [ "$target" = migrations ]; then
        grep -q -- '--- PASS: TestBEMigrate001PostgresFromZeroUpgradeIsolationAndRollback' /evidence/postgres-migrations.log
      fi
	  if [ "$target" = media ]; then
	    grep -q -- '--- PASS: TestDueVersionCleanerDisposableDatabaseAndObjectStore' /evidence/postgres-media.log
	  fi
      printf '%s PASSED\n' "$target" >> /evidence/postgres-results.log
    done
    ;;
  dependencies)
    cd tests/integration
    go test -tags=integration -count=1 -timeout=20m .
    ;;
  scan)
    go list -deps ./cmd/... ./internal/... > /evidence/package-dependencies.txt
    if grep -Eq '^golang.org/x/crypto/openpgp(/|$)' /evidence/package-dependencies.txt; then
      printf 'Unmaintained OpenPGP packages must not be imported. See GO-2026-5932.\n' >&2
      exit 1
    fi
    go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./cmd/... ./internal/...
    ;;
  *) printf 'Expected manifest, quality, postgres, dependencies or scan\n' >&2; exit 2 ;;
esac
