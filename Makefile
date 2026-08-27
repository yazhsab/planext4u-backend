GO ?= go

.PHONY: build contract-check contract-generate fmt local-down local-status local-up run test test-integration test-race vet verify

build:
	$(GO) build ./...

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

local-up:
	docker compose -f deploy/local/compose.yaml up -d --build --wait

run:
	$(GO) run ./cmd/platform

test:
	$(GO) test ./...

test-integration:
	cd tests/integration && DOCKER_AUTH_CONFIG='{"auths":{}}' $(GO) test -tags=integration -count=1 -timeout=10m .

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

verify: fmt contract-check vet test test-race build
