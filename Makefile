.PHONY: build build-ci install-quality-tools test test-no-race test-race lint vet fmt fmt-check gosec govulncheck ci-checks check clean coverage coverage-ratchet coverage-bump coverage-history catalog-dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BINARY_NAME := ddx-agent
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

build:
	go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/agent

catalog-dist:
	go run ./cmd/catalogdist \
		--manifest internal/modelcatalog/catalog/models.yaml \
		--out website/static/catalog \
		--channel stable \
		--min-agent-version "$${MIN_AGENT_VERSION:-$$(git describe --tags --abbrev=0 --match 'v*' 2>/dev/null || echo dev)}"

build-ci:
	go build ./...

install-quality-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest

test:
	CGO_ENABLED=1 go test -race ./...

test-no-race:
	go test -count=1 ./...

test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

test-integration:
	CGO_ENABLED=1 go test -race -tags=integration ./...

test-e2e:
	CGO_ENABLED=1 go test -race -tags=e2e ./...

test-fuzz:
	go test -fuzz=. -fuzztime=30s ./...

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	gofmt -l . | grep -v '^\.claude/' | grep -v '^\.ddx/' | grep . && exit 1 || true

fmt-check:
	@unformatted="$$(gofmt -l . | grep -v '^\.claude/' | grep -v '^\.ddx/')"; \
	if [ -n "$$unformatted" ]; then \
		echo "Files not formatted:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

gosec:
	gosec -exclude-dir=.claude -exclude-dir=.ddx ./...

govulncheck:
	govulncheck ./...

ci-checks: build-ci vet lint gosec govulncheck fmt-check test-no-race test-race
	@echo "All CI checks passed."

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
	rm -f $(BINARY_NAME)
	go clean ./...
