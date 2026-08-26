GO ?= go

.PHONY: build fmt run test test-race vet verify

build:
	$(GO) build ./...

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

verify: fmt vet test test-race build
