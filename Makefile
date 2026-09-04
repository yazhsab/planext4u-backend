GO ?= go
GO_PACKAGES := ./cmd/... ./internal/...

.PHONY: admin-verify build container-build contract-check contract-generate fmt identity-schema-local infra-check local-down local-seed-transaction local-service-logins local-status local-up migration-check migrate-local run run-admin run-customer-web run-identity run-transaction test test-admin-integration test-audit-integration test-booking-integration test-catalog-integration test-checkout-integration test-commerce-integration test-configuration-integration test-customer-web-integration test-emergency-integration test-food-integration test-fulfillment-integration test-gateway-integration test-governance-integration test-identity-integration test-integration test-inventory-integration test-local-verticals-integration test-media-integration test-messaging-integration test-migrations-integration test-notification-integration test-order-integration test-payment-integration test-race test-social-integration test-support-integration test-supply-integration test-wallet-integration vet verify

admin-verify:
	cd admin-web && npm ci && npm run verify && npm run test:e2e

build:
	$(GO) build $(GO_PACKAGES)

container-build:
	docker build --build-arg VERSION=local --build-arg COMMIT="$$(git rev-parse --short HEAD)" --tag planext4u-backend:local .

contract-check:
	$(GO) run ./cmd/responsecontractgen -check
	$(GO) run ./cmd/contractgen -check
	$(GO) test ./internal/contracts/...

contract-generate:
	$(GO) run ./cmd/responsecontractgen
	$(GO) run ./cmd/contractgen

fmt:
	@files="$$(gofmt -l cmd internal)"; \
	if [ -n "$$files" ]; then \
		echo "The following Go files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

local-down:
	docker compose -f deploy/local/compose.yaml down --volumes --remove-orphans

local-status:
	docker compose -f deploy/local/compose.yaml ps

local-service-logins:
	docker compose -f deploy/local/compose.yaml exec -T postgres psql -v ON_ERROR_STOP=1 -U planext4u_local -d planext4u_local < deploy/local/create-service-logins.sql

local-seed-transaction:
	docker compose -f deploy/local/compose.yaml exec -T postgres psql -v ON_ERROR_STOP=1 -U planext4u_local -d planext4u_local < deploy/local/seed-transaction.sql

local-up:
	docker compose -f deploy/local/compose.yaml up -d --build --wait

run:
	$(GO) run ./cmd/platform

run-identity:
	$(GO) run ./cmd/identity

run-transaction:
	$(GO) run ./cmd/transaction

run-admin:
	$(GO) run ./cmd/admin

run-customer-web:
	$(GO) run ./cmd/customer-web

identity-schema-local:
	MIGRATION_DATABASE_URL_FILE=.local/identity/database.url $(GO) run ./cmd/migrate -service platform
	MIGRATION_DATABASE_URL_FILE=.local/identity/database.url $(GO) run ./cmd/migrate -service identity

infra-check:
	./scripts/check-infra.sh

migration-check:
	$(GO) run ./cmd/migrationcheck

migrate-local:
	MIGRATION_DATABASE_URL_FILE=.local/migrations/database.url $(GO) run ./cmd/migrate -service all
	$(MAKE) local-service-logins

test:
	$(GO) test $(GO_PACKAGES)

test-integration:
	cd tests/integration && DOCKER_AUTH_CONFIG='{"auths":{}}' $(GO) test -tags=integration -count=1 -timeout=10m .

test-gateway-integration:
	REDIS_TEST_URL="$${REDIS_TEST_URL:-redis://127.0.0.1:63790/0}" $(GO) test -tags=integration -count=1 ./internal/gateway

test-audit-integration:
	AUDIT_DATABASE_TEST_URL="$${AUDIT_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/audit

test-booking-integration:
	BOOKING_DATABASE_TEST_URL="$${BOOKING_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/booking

test-admin-integration:
	ADMIN_DATABASE_TEST_URL="$${ADMIN_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/adminops
	ADMIN_DATABASE_TEST_URL="$${ADMIN_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/adminshell

test-customer-web-integration:
	CUSTOMER_WEB_DATABASE_TEST_URL="$${CUSTOMER_WEB_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/customerweb

test-catalog-integration:
	CATALOG_DATABASE_TEST_URL="$${CATALOG_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/catalog

test-checkout-integration:
	CHECKOUT_DATABASE_TEST_URL="$${CHECKOUT_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/checkout

test-commerce-integration:
	COMMERCE_DATABASE_TEST_URL="$${COMMERCE_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/commerce

test-configuration-integration:
	CONFIGURATION_DATABASE_TEST_URL="$${CONFIGURATION_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/configcms

test-food-integration:
	FOOD_DATABASE_TEST_URL="$${FOOD_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/food

test-emergency-integration:
	EMERGENCY_DATABASE_TEST_URL="$${EMERGENCY_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/emergency

test-fulfillment-integration:
	FULFILLMENT_DATABASE_TEST_URL="$${FULFILLMENT_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/fulfillment

test-local-verticals-integration:
	LOCAL_VERTICALS_DATABASE_TEST_URL="$${LOCAL_VERTICALS_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/localverticals

test-social-integration:
	SOCIAL_DATABASE_TEST_URL="$${SOCIAL_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/social

test-support-integration:
	SUPPORT_DATABASE_TEST_URL="$${SUPPORT_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/support

test-governance-integration:
	GOVERNANCE_DATABASE_TEST_URL="$${GOVERNANCE_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/governance

test-identity-integration:
	@mkdir -p coverage
	IDENTITY_DATABASE_TEST_URL="$${IDENTITY_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 -coverprofile=coverage/identity.out ./internal/identity
	$(GO) run ./cmd/coveragecheck -profile coverage/identity.out -min 80

test-inventory-integration:
	INVENTORY_DATABASE_TEST_URL="$${INVENTORY_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/inventory

test-media-integration:
	MEDIA_DATABASE_TEST_URL="$${MEDIA_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/media

test-messaging-integration:
	MESSAGING_DATABASE_TEST_URL="$${MESSAGING_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" NATS_TEST_URL="$${NATS_TEST_URL:-nats://127.0.0.1:42220}" $(GO) test -tags=integration -count=1 ./internal/messaging

test-notification-integration:
	NOTIFICATION_DATABASE_TEST_URL="$${NOTIFICATION_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/notification

test-order-integration:
	ORDER_DATABASE_TEST_URL="$${ORDER_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/order

test-payment-integration:
	PAYMENT_DATABASE_TEST_URL="$${PAYMENT_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/payment

test-supply-integration:
	SUPPLY_DATABASE_TEST_URL="$${SUPPLY_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/supply

test-wallet-integration:
	WALLET_DATABASE_TEST_URL="$${WALLET_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/wallet

test-migrations-integration:
	MIGRATION_DATABASE_TEST_URL="$${MIGRATION_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/migrations

test-race:
	$(GO) test -race $(GO_PACKAGES)

vet:
	$(GO) vet $(GO_PACKAGES)

verify: fmt contract-check migration-check vet test test-race build
