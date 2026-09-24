# Developer shortcuts. Run `make` or `make help` to list targets.

COMPOSE_FILE ?= deploy/docker-compose.yml
COMPOSE      := docker compose -f $(COMPOSE_FILE)

# Keep in sync with the golangci-lint step in .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION ?= v2.14.0
# Prefer the copy `make tools` installs (GOBIN or GOPATH/bin) over any other
# golangci-lint in PATH, which may be older than the pinned version.
GOBIN_DIR     := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
GOLANGCI_LINT ?= $(or $(wildcard $(GOBIN_DIR)/golangci-lint),$(shell command -v golangci-lint 2>/dev/null))

.DEFAULT_GOAL := help

.PHONY: help tools fmt fmt-check vet golangci-lint lint test test-integration build \
	up down logs docker-build compose-file check clean

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# `go env GOVERSION` run here resolves the toolchain go.mod asks for (with
# the default GOTOOLCHAIN=auto). Building golangci-lint with it lets it lint
# this module; a plain `go install` would use the local Go, which may be older.
tools: ## Install the pinned golangci-lint (built with the module's Go) into GOBIN
	GOTOOLCHAIN=$$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@"$(GOBIN_DIR)/golangci-lint" version

fmt: ## Format all Go files in place (gofmt -w)
	gofmt -w .

fmt-check: ## Fail and list files that need gofmt (same check as CI)
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

golangci-lint: ## Run golangci-lint (version pinned in GOLANGCI_LINT_VERSION)
	@if [ -z "$(GOLANGCI_LINT)" ]; then \
		echo "golangci-lint not found. Install the pinned version with:" >&2; \
		echo "  make tools" >&2; \
		exit 1; \
	fi
	@want="$(GOLANGCI_LINT_VERSION)"; \
	if ! "$(GOLANGCI_LINT)" version 2>/dev/null | grep -q "version $${want#v} "; then \
		echo "warning: $(GOLANGCI_LINT) is not $$want (run: make tools)" >&2; \
	fi
	"$(GOLANGCI_LINT)" run ./...

lint: fmt-check vet golangci-lint ## Run all static checks (gofmt, go vet, golangci-lint)

test: ## Run all tests with the race detector (needs ffmpeg in PATH)
	go test -race -count=1 ./...

test-integration: ## Run the integration suite verbosely (needs ffmpeg in PATH)
	go test -v -race -count=1 ./tests/integration/

build: ## Build all packages
	go build ./...

compose-file:
	@if [ ! -f "$(COMPOSE_FILE)" ]; then \
		echo "no compose file yet: $(COMPOSE_FILE) — added in Phase 2.1" >&2; \
		exit 1; \
	fi

up: compose-file ## Build and start the local stack, wait until it is healthy
	$(COMPOSE) up -d --build --wait

down: compose-file ## Stop the local stack and remove its volumes
	$(COMPOSE) down -v

logs: compose-file ## Follow the local stack logs
	$(COMPOSE) logs -f

docker-build: compose-file ## Build the service images of the local stack
	$(COMPOSE) build

check: lint test ## Run everything CI runs; use before pushing

clean: ## Remove coverage output
	rm -f coverage.out coverage.txt
