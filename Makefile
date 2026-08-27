GO ?= go

.PHONY: build container-build contract-check contract-generate fmt identity-schema-local infra-check local-down local-service-logins local-status local-up migration-check migrate-local run run-identity test test-gateway-integration test-identity-integration test-integration test-migrations-integration test-race vet verify

build:
	$(GO) build ./...

container-build:
	docker build --build-arg VERSION=local --build-arg COMMIT="$$(git rev-parse --short HEAD)" --tag planext4u-backend:local .

contract-check:
	$(GO) run ./cmd/contractgen -check
	$(GO) test ./internal/contracts/...

contract-generate:
	$(GO) run ./cmd/contractgen

fmt:
	@files="$$(gofmt -l .)"; \
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

local-up:
	docker compose -f deploy/local/compose.yaml up -d --build --wait

run:
	$(GO) run ./cmd/platform

run-identity:
	$(GO) run ./cmd/identity

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
	$(GO) test ./...

test-integration:
	cd tests/integration && DOCKER_AUTH_CONFIG='{"auths":{}}' $(GO) test -tags=integration -count=1 -timeout=10m .

test-gateway-integration:
	REDIS_TEST_URL="$${REDIS_TEST_URL:-redis://127.0.0.1:63790/0}" $(GO) test -tags=integration -count=1 ./internal/gateway

test-identity-integration:
	@mkdir -p coverage
	IDENTITY_DATABASE_TEST_URL="$${IDENTITY_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 -coverprofile=coverage/identity.out ./internal/identity
	$(GO) run ./cmd/coveragecheck -profile coverage/identity.out -min 80

test-migrations-integration:
	MIGRATION_DATABASE_TEST_URL="$${MIGRATION_DATABASE_TEST_URL:-postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable}" $(GO) test -tags=integration -count=1 ./internal/migrations

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

verify: fmt contract-check migration-check vet test test-race build
