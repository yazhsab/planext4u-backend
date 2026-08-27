GO ?= go

.PHONY: build contract-check contract-generate fmt run test test-race vet verify

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

run:
	$(GO) run ./cmd/platform

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

verify: fmt contract-check vet test test-race build
