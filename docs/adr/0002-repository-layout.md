# 0002. Repository layout: single-module monorepo

- Status: Accepted
- Date: 2026-09-24

## Context

The rebuild has three services — **api**, **worker**, **notifier** (PLAN.md
§4) — that share a domain (users, videos/jobs, job states, supported
formats) and message contracts. The repo is one Go module (`video-processor`,
root `go.mod`); `tests/integration` and CI (`gofmt -l .`, `go vet ./...`,
`go test -race ./...`) run from the module root. PLAN.md Phase 0 suggested
`services/<name>` + `pkg/`; this ADR settles the layout.

Options considered: one module per service (`services/*/go.mod`, go.work),
or one module with one `cmd/` per service.

## Decision

A **single Go module monorepo**, with the module kept in the root `go.mod`:

```
cmd/
  api/            main package: wiring only (config, adapters, HTTP server)
  worker/         main package: wiring only (consumer, ffmpeg, storage)
  notifier/       main package: wiring only (consumer, mailer)
internal/
  domain/         entities, value objects, JobStatus, format validation
  app/            use cases and ports (interfaces)
  adapters/
    http/         Gin handlers, middleware (auth, logging, metrics)
    postgres/     repositories (pgx)
    rabbitmq/     publisher / consumer, topology
    storage/      MinIO / S3
    ffmpeg/       frame extractor
    zip/          archiver
    mailer/       SMTP
  platform/
    config/       env configuration
    logging/      slog setup, correlation ids
db/migrations/    versioned SQL (D2)
deploy/           compose files, k8s manifests, RabbitMQ definitions, MinIO bootstrap
docs/             architecture, ADRs, openapi.yaml
tests/integration/  black-box suite (the spec, ADR 0001)
```

Rules: `cmd/*` stay thin; `internal/domain` imports nothing from the project;
`internal/app` depends only on `domain` and its own ports; adapters implement
ports and never import each other; services never import another service's
`cmd/` package.

Why:
- One shared domain and message contract, without versioning internal
  libraries across modules.
- One `go.mod`, one CI pipeline, one `go test ./...` — simpler for a
  hackathon team, and the existing `tests/integration` package and CI keep
  working unchanged.
- Services still build and deploy independently: one binary per `cmd/`, one
  image per service, scaled separately (RT2).
- `internal/` keeps the code private to the module.

## Consequences

- Dependencies are shared: the worker image's binary only links what
  `cmd/worker` imports, but all services use the same versions of every
  library.
- Per-service Dockerfiles build with `go build ./cmd/<service>` from the repo
  root (Phase 4); CI can later add a per-service matrix (Phase 6).
- The integration harness currently builds "the app from the module root".
  That changes in Phase 1: the harness targets the compose stack via
  `BASE_URL` (and `MAILHOG_URL`) instead of building a binary.
- PLAN.md's `services/` + `pkg/` wording is superseded by this layout.
- Splitting into several modules later stays possible, since boundaries
  already follow `cmd/` and `internal/` packages.
