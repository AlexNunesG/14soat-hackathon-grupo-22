# Developer shortcuts. Run `make` or `make help` to list targets.

COMPOSE_FILE ?= deploy/docker-compose.yml
COMPOSE      := docker compose -f $(COMPOSE_FILE)

.DEFAULT_GOAL := help

.PHONY: help fmt fmt-check vet lint test test-integration cover build \
	up down logs compose-file check clean

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

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

lint: fmt-check vet ## Run all static checks

test: ## Run all tests with the race detector (needs ffmpeg in PATH)
	go test -race -count=1 ./...

test-integration: ## Run the integration suite verbosely (needs ffmpeg in PATH)
	go test -v -race -count=1 ./tests/integration/

cover: ## Run the integration suite with app coverage and print a summary
	COVERAGE_OUT=coverage.out go test -count=1 ./tests/integration/
	@if [ -f coverage.out ]; then go tool cover -func=coverage.out; \
	else echo "no coverage.out: no app was built"; fi

build: ## Build all packages
	go build ./...

compose-file:
	@if [ ! -f "$(COMPOSE_FILE)" ]; then \
		echo "no compose file yet: $(COMPOSE_FILE) — added in Phase 2.1" >&2; \
		exit 1; \
	fi

up: compose-file ## Start the local stack and wait until it is healthy
	$(COMPOSE) up -d --wait

down: compose-file ## Stop the local stack and remove its volumes
	$(COMPOSE) down -v

logs: compose-file ## Follow the local stack logs
	$(COMPOSE) logs -f

check: lint test ## Run everything CI runs; use before pushing

clean: ## Remove coverage output
	rm -f coverage.out coverage.txt
