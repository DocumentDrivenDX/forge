.PHONY: build test lint vet fmt check clean coverage coverage-ratchet coverage-bump coverage-history

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

build:
	go build $(LDFLAGS) ./cmd/forge

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-e2e:
	go test -race -tags=e2e ./...

test-fuzz:
	go test -fuzz=. -fuzztime=30s ./...

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	gofmt -l . | grep . && exit 1 || true

gosec:
	gosec ./...

govulncheck:
	govulncheck ./...

check: fmt vet lint test coverage-ratchet
	@echo "All checks passed."

# Coverage targets
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@echo ""
	@echo "Full coverage report: coverage.html"
	go tool cover -html=coverage.out -o coverage.html

coverage-ratchet:
	@echo "Running coverage ratchet check..."
	@go run scripts/coverage-ratchet.go

coverage-bump: coverage-ratchet
	@echo "Auto-bumping coverage floors where coverage exceeds floor by >10%..."
	@go run scripts/coverage-ratchet.go --bump

coverage-history:
	@echo "Coverage history (from .helix-ratchets/coverage-floor.json):"
	@cat .helix-ratchets/coverage-floor.json | jq '.history'

coverage-trend: coverage-ratchet
	@echo "Coverage trend from history:"
	@go run scripts/coverage-ratchet.go --trend

clean:
	rm -f forge
	go clean ./...
