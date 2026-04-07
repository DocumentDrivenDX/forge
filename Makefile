.PHONY: build test lint vet fmt check clean

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

check: fmt vet lint test
	@echo "All checks passed."

clean:
	rm -f forge
	go clean ./...
