# 0001 — Monorepo layout

## Context

The rewrite splits the monolithic prototype into four Go services (`auth-service`,
`video-service`, `worker-service`, `notification-service`) plus a gateway and shared
infrastructure config. These services are developed by the same small team, over the same short
hackathon timeline, and change together frequently (e.g. a new field in the job status model
touches `video-service`, `worker-service`, and potentially `notification-service` in the same
change). We need to decide whether each service lives in its own repository (polyrepo) or all of
them live together (monorepo).

## Decision

Use a single monorepo containing all services, shared packages (`/pkg`), deployment
configuration (`/deploy`), documentation (`/docs`), and CI workflows
(`.github/workflows`). Each service is its own Go module, wired together locally via a root
`go.work` file, so services remain independently buildable/deployable despite sharing a
repository.

## Consequences

- Cross-service changes (e.g. a shared message contract or a docker-compose wiring change) land
  in a single commit/PR and a single CI run, instead of being coordinated across multiple repos.
- One `git clone` gets a contributor the entire system, which matters for a short hackathon
  timeline and for grading/review.
- `go.work` lets each service keep its own `go.mod`/dependency set while still being built and
  tested together locally.
- Tradeoff: CI must be scoped per-service (e.g. path filters or a build matrix) to avoid
  rebuilding/retesting every service on every change; this is deferred to the CI/CD delivery item.
- Tradeoff: a monorepo makes it easier to accidentally couple services through shared code; we
  mitigate this by keeping `/pkg` limited to genuinely cross-cutting concerns (message contracts,
  config loading) rather than domain logic.
