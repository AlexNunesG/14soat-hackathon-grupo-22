# Developer shortcuts. Run `make` or `make help` to list targets.

COMPOSE_FILE ?= deploy/docker-compose.yml
COMPOSE      := docker compose -f $(COMPOSE_FILE)

# Keep in sync with the prometheus service in deploy/docker-compose.yml.
PROMETHEUS_IMAGE ?= prom/prometheus:v3.5.0
PROMTOOL         := docker run --rm -v "$(CURDIR)/deploy/prometheus:/etc/prometheus:ro" --entrypoint promtool $(PROMETHEUS_IMAGE)

# Keep in sync with the golangci-lint step in .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION ?= v2.14.0
# Keep in sync with the govulncheck step in .github/workflows/ci.yml.
GOVULNCHECK_VERSION ?= v1.8.0
# Prefer the copy `make tools` installs (GOBIN or GOPATH/bin) over any other
# golangci-lint/govulncheck in PATH, which may be older than the pinned version.
GOBIN_DIR     := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
GOLANGCI_LINT ?= $(or $(wildcard $(GOBIN_DIR)/golangci-lint),$(shell command -v golangci-lint 2>/dev/null))
GOVULNCHECK   ?= $(or $(wildcard $(GOBIN_DIR)/govulncheck),$(shell command -v govulncheck 2>/dev/null))

# Coverage target for `make coverage` (Phase 5.1): combined statement
# coverage of internal/domain + internal/app, the packages the plan names
# ("domain/use cases").
COVERAGE_THRESHOLD ?= 80

.DEFAULT_GOAL := help

.PHONY: help tools fmt fmt-check vet golangci-lint lint test test-integration coverage \
	vulncheck build up down migrate logs docker-build compose-file obs-check k8s-check \
	check clean loadtest

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# `go env GOVERSION` run here resolves the toolchain go.mod asks for (with
# the default GOTOOLCHAIN=auto). Building golangci-lint/govulncheck with it
# lets them analyze this module; a plain `go install` would use the local
# Go, which may be older.
tools: ## Install the pinned golangci-lint and govulncheck (built with the module's Go) into GOBIN
	GOTOOLCHAIN=$$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOTOOLCHAIN=$$(go env GOVERSION) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@"$(GOBIN_DIR)/golangci-lint" version
	@"$(GOBIN_DIR)/govulncheck" -version

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

coverage: ## Unit-test coverage for internal/domain + internal/app; fails below COVERAGE_THRESHOLD% (default 80)
	go test -race -coverprofile=coverage.out -covermode=atomic ./internal/domain/... ./internal/app/...
	go tool cover -func=coverage.out
	@pct=$$(go tool cover -func=coverage.out | awk '/^total:/ { gsub("%","",$$3); print $$3 }'); \
	echo "combined coverage (internal/domain + internal/app): $$pct%"; \
	awk -v p="$$pct" -v t="$(COVERAGE_THRESHOLD)" 'BEGIN { exit (p + 0 >= t + 0) ? 0 : 1 }' || { \
		echo "coverage $$pct% is below the $(COVERAGE_THRESHOLD)% target for internal/domain + internal/app" >&2; \
		exit 1; \
	}

vulncheck: ## Scan the module for known vulnerabilities (golang.org/x/vuln/cmd/govulncheck)
	@if [ -z "$(GOVULNCHECK)" ]; then \
		echo "govulncheck not found. Install the pinned version with:" >&2; \
		echo "  make tools" >&2; \
		exit 1; \
	fi
	"$(GOVULNCHECK)" ./...

build: ## Build all packages
	go build ./...

compose-file:
	@if [ ! -f "$(COMPOSE_FILE)" ]; then \
		echo "no compose file yet: $(COMPOSE_FILE) — added in Phase 2.1" >&2; \
		exit 1; \
	fi

up: compose-file ## Build and start the local stack (incl. Prometheus :9091, Grafana :3000), wait until healthy
	$(COMPOSE) up -d --build --wait

down: compose-file ## Stop the local stack and remove its volumes
	$(COMPOSE) down -v

migrate: compose-file ## Apply pending DB migrations to the compose postgres (docs/database.md)
	$(COMPOSE) run --rm --build migrate

logs: compose-file ## Follow the local stack logs
	$(COMPOSE) logs -f

docker-build: compose-file ## Build the service images of the local stack
	$(COMPOSE) build

obs-check: ## Validate the Prometheus config and alert rules with promtool (docker)
	$(PROMTOOL) check config /etc/prometheus/prometheus.yml
	$(PROMTOOL) check rules $(patsubst deploy/prometheus/%,/etc/prometheus/%,$(wildcard deploy/prometheus/rules/*.yml))

# overlays/prod needs a real deploy/k8s/overlays/prod/secrets.env (gitignored,
# see deploy/k8s/README.md); it is rendered only when that file exists.
k8s-check: ## Validate the Kubernetes manifests render (kubectl kustomize, offline)
	@command -v kubectl >/dev/null || { echo "kubectl not found: see https://kubernetes.io/docs/tasks/tools/#kubectl" >&2; exit 1; }
	kubectl kustomize deploy/k8s/base >/dev/null
	kubectl kustomize deploy/k8s/overlays/dev >/dev/null
	kubectl kustomize deploy/k8s/keda >/dev/null
	@if [ -f deploy/k8s/overlays/prod/secrets.env ]; then \
		kubectl kustomize deploy/k8s/overlays/prod >/dev/null; \
	else \
		echo "skipping overlays/prod render: deploy/k8s/overlays/prod/secrets.env not present (see deploy/k8s/README.md)"; \
	fi

check: lint obs-check k8s-check test coverage vulncheck ## Run everything CI runs; use before pushing

# Compose's default network name (project-dir_default); override if the
# stack was started with COMPOSE_PROJECT_NAME set, e.g.
#   make loadtest LOADTEST_NETWORK=myproject_default
LOADTEST_NETWORK ?= video-processor_default
LOADTEST_OUT_DIR := docs/loadtest
K6_IMAGE ?= grafana/k6:latest

loadtest: up ## Run the k6 spike load test (RF2, deploy/loadtest/) against the local stack; results in docs/loadtest/
	@mkdir -p $(LOADTEST_OUT_DIR)
	@echo "--- phase 1/2: spike load (deploy/loadtest/spike.js) ---"
	@bash -c 'set -o pipefail; docker run --rm --network $(LOADTEST_NETWORK) -u root \
		-v "$(CURDIR)/deploy/loadtest:/scripts:ro" \
		-v "$(CURDIR)/$(LOADTEST_OUT_DIR):/out" \
		-e BASE_URL=http://api:8080 \
		-e LOADTEST_OUT_DIR=/out \
		$(K6_IMAGE) run /scripts/spike.js 2>&1 | tee $(LOADTEST_OUT_DIR)/spike.log'
	@echo "--- phase 2/2: confirm every accepted video reaches DONE/FAILED (deploy/loadtest/confirm.js) ---"
	@bash -c 'set -o pipefail; docker run --rm --network $(LOADTEST_NETWORK) -u root \
		-v "$(CURDIR)/deploy/loadtest:/scripts:ro" \
		-v "$(CURDIR)/$(LOADTEST_OUT_DIR):/out" \
		-e BASE_URL=http://api:8080 \
		-e LOADTEST_OUT_DIR=/out \
		-e LOADTEST_SPIKE_LOG=/out/spike.log \
		$(K6_IMAGE) run /scripts/confirm.js 2>&1 | tee $(LOADTEST_OUT_DIR)/confirm.log'
	@echo "results: $(LOADTEST_OUT_DIR)/{spike,confirm}-report.md, {spike,confirm}-summary.json, {spike,confirm}.log"

clean: ## Remove coverage output
	rm -f coverage.out coverage.txt
