# FIAP X Video Processor — Hackathon Plan (SOAT Phase 5)

Source: [`POSTECH_-_SOAT_-_Fase_5_-_Hacka.pdf`](./POSTECH_-_SOAT_-_Fase_5_-_Hacka.pdf)

This is a living checklist. Tick items (`- [x]`) in the same PR that delivers them,
and add a short note or link (PR, file) when useful. Keep the **Requirement
traceability** table in sync: a requirement is done only when every item that
maps to it is ticked.

## Ground rules: the tests are the spec

These rules apply to every phase and every PR, human or AI agent.

1. **The integration tests are the specification.** Once Phase 1 is merged,
   `tests/integration/` (with `docs/openapi.yaml`) defines what the system must
   do. When code and tests disagree, the code is wrong.
2. **The challenge PDF wins over the tests.** The only valid reason to change
   the expected behavior of a test is that it contradicts, or fails to cover,
   a requirement in the PDF (§2). In that case:
   - fix the test in its own PR, separate from implementation code;
   - explain in the PR description which requirement (RF/RT/D id) the test
     got wrong and why;
   - update `docs/openapi.yaml` and this plan in the same PR.
3. **Avoid changing tests.** Do not edit an assertion, timeout, status code or
   payload to make an implementation pass. Allowed without the process above:
   fixing a bug in the harness or a helper that does not change what is
   asserted, and adding *new* tests for uncovered behavior.
4. **Enable tests as features are implemented.** Each implementation PR
   deletes the `notImplemented(t)` line of every test it makes pass — no
   later, no earlier. A test is never enabled without the code that makes it
   pass, and a feature is never merged while its tests are still skipped.
5. **Never skip, disable or weaken a test to get green CI.** Re-adding
   `notImplemented(t)` to an enabled test counts as disabling it.

---

## 1. The challenge in one paragraph

FIAP X has a demo app that takes a video and returns its frames as a `.zip`
(the original `main.go`: one Gin process, synchronous `ffmpeg` call, files on
local disk, no auth). Investors want a real product: users upload videos and
download the zip later. We must rebuild it with proper software architecture:
architecture design, microservices, software quality, messaging, and the other
concepts from the course.

## 2. Requirements (from the PDF)

### Functional (essential features)
- **RF1** Process more than one video at the same time.
- **RF2** Never lose a request during load peaks.
- **RF3** The system is protected by username and password.
- **RF4** List the status of a user's videos.
- **RF5** On error, notify the user (e-mail or another channel).

### Technical (architecture & infrastructure)
- **RT1** Persist the data.
- **RT2** Scalable architecture.
- **RT3** Versioned on GitHub.
- **RT4** Tests that guarantee quality.
- **RT5** CI/CD.

### Recommended stack
- Containers: Docker + Kubernetes or Docker Compose.
- Messaging: RabbitMQ, Kafka or similar.
- Database: PostgreSQL + Redis (cache), or another of the group's choice.
- Monitoring: Prometheus + Grafana, ELK, or similar.
- CI/CD: GitHub Actions or similar.

### Deliverables
- **D1** Architecture documentation.
- **D2** Database creation script (and scripts for other resources used).
- **D3** GitHub link(s) of the project(s).
- **D4** Video of at most 10 minutes showing: the documentation, the chosen
  architecture, and the project working.

## 3. Starting point (repo state)

- Go module `video-processor` (was Go 1.21 + Gin; now Go 1.27, no deps yet). The original `main.go` and
  `Dockerfile` were removed in #8 (still in git history: `git show HEAD~1:main.go`).
- `tests/integration/`: 28 black-box HTTP tests describing the **legacy**
  contract (`GET /`, `POST /upload` synchronous, `GET /api/status`,
  `GET /download/:filename`, `/uploads/*`, `/outputs/*`, CORS, startup/shutdown).
  All are skipped with `notImplemented(t)`; each PR removes that line for what it
  implements.
- `.github/workflows/ci.yml`: gofmt, go vet, `go test -race` with ffmpeg, coverage.
  No CD yet.

## 4. Proposed architecture

```
                ┌──────────────┐
  Browser/CLI ─▶│  API service │── JWT auth, upload, list, download
                └──────┬───────┘
       store video     │ insert job (PENDING)      publish "video.uploaded"
   ┌───────────────────┼─────────────────────────────────┐
   ▼                   ▼                                 ▼
┌───────┐       ┌────────────┐                    ┌────────────┐
│  S3*  │       │ PostgreSQL │◀── status updates ─│  RabbitMQ  │
│ (S3)  │       └────────────┘                    └─────┬──────┘
└───▲───┘              ▲   ▲ cache                      │ consume (N replicas,
    │                  │   └── Redis                    │ prefetch, manual ack)
    │           ┌──────┴───────┐                        ▼
    └───────────│ Worker (xN)  │◀───────────────────────┘
  read video,   │ ffmpeg → zip │── publish "video.processed" / "video.failed"
  write zip     └──────────────┘                        │
                                                        ▼
                                                ┌──────────────┐
                                                │ Notification │── SMTP (MailHog locally)
                                                │   service    │
                                                └──────────────┘
      Prometheus scrapes /metrics of every service → Grafana dashboards
```

Services (Go, one module or one folder each under `services/`):

| Service | Responsibility | Scales by |
|---|---|---|
| **api** | Sign-up/login (bcrypt + JWT), upload to object storage, create job, publish message, list user's videos, presigned/streamed download | Replicas behind LB (stateless) |
| **worker** | Consume `video.uploaded`, run ffmpeg, build zip, upload zip, update status, publish result | Replicas / HPA on queue depth |
| **notifier** | Consume `video.failed` (and optionally `video.processed`), send e-mail | Replicas |

Key decisions (record each as an ADR in `docs/adr/`):
- **Async processing via queue** → RF1 (N workers in parallel) and RF2 (durable
  queue + persistent messages + manual ack + DLQ; API only acks the upload after
  the message is confirmed by the broker — publisher confirms).
- **Object storage (S3 API; SeaweedFS in compose, see ADR 0003 — MinIO no longer
  publishes images)** instead of local disk → stateless API/workers → RT2.
- **PostgreSQL** as source of truth for users and jobs → RT1; **Redis** for
  status-list cache and rate limiting (optional).
- **Idempotent worker**: job id as message id; skip if job already `DONE`;
  retries with backoff, then DLQ + `FAILED` status + notification.
- Job states: `PENDING → PROCESSING → DONE | FAILED`.

## 5. Decision: replace the legacy test contract

**Decided (2026-09-24):** the legacy contract is dropped. The work is split in
two ordered steps:

1. **Phase 1 — Rebuild the tests.** Rewrite `tests/integration/` so it
   describes the *new* behavior (v1 API, authentication, async processing,
   per-user status, notifications). Legacy tests are deleted. Every new test
   starts with `notImplemented(t)`, so the suite stays green and doubles as the
   executable spec.
2. **Phase 2 — Implement.** Build the services until the new tests pass. Each
   implementation PR deletes the `notImplemented(t)` lines of the tests it makes
   pass (same rule as today). The phase is done when
   `grep -rn 'notImplemented(t)$' tests/integration/` returns nothing.

After Phase 1, the new suite is the spec and follows the **Ground rules**
at the top of this plan.

- [x] Record this decision in [`docs/adr/0001-replace-legacy-test-contract.md`](../docs/adr/0001-replace-legacy-test-contract.md).
- [x] Copy the Ground rules into [`CLAUDE.md`](../CLAUDE.md).
- [x] Copy the Ground rules into `tests/integration/README.md` (Phase 1.4).

---

## 6. Checklist by phase

### Phase 0 — Project setup
- [x] Create `.ai-agents/` with the challenge PDF and this plan.
- [x] Decide repo layout and record it in an ADR: single-module monorepo,
      `cmd/{api,worker,notifier}` + `internal/…`
      ([ADR 0002](../docs/adr/0002-repository-layout.md)).
- [x] Upgrade Go version (1.21 is EOL) and pin tool versions: `go 1.27`
      (latest stable); gin dropped from `go.mod` until code imports it
      (gin ≥ v1.12 is compatible). Linter versions are pinned in the lint step.
- [x] Add `Makefile` (`make lint test up down`; `make check` before pushing) and `.editorconfig`.
- [x] Add `golangci-lint` config and run it in CI (v2.14.0 pinned in `Makefile` and `ci.yml`; `make tools` installs it).
- [x] Remove the stray `__MACOSX/` folder and add it to `.gitignore` (plus `.DS_Store`, `._*`, `/bin/`, `*.test`, `.env`).

### Phase 1 — Rebuild the integration tests for the new behavior (tests first)

Goal: a black-box suite that defines the v1 contract before any code exists.
No production code in this phase.

**1.1 Contract**
- [x] Write the v1 contract in [`docs/openapi.yaml`](../docs/openapi.yaml)
      (routes, payloads, status codes, error format
      `{"error":{"code","message"}}`). The tests assert exactly this file.
- [x] Contract summary to cover:
  - `POST /api/v1/auth/register` → 201; 409 duplicate e-mail; 400 invalid input.
  - `POST /api/v1/auth/login` → 200 `{token}`; 401 wrong credentials.
  - `POST /api/v1/videos` (multipart, one or more `videos` files, Bearer
    token) → 202 `[{id, status: "PENDING"}]`; 400 missing file / unsupported
    extension (`mp4, avi, mov, mkv, wmv, flv, webm`, any case); 401 no token.
  - `GET /api/v1/videos` → 200 paginated list of *the caller's* videos
    `{id, original_name, status, frame_count, error_message, created_at,
    updated_at}`.
  - `GET /api/v1/videos/{id}` → 200; 404 for another user's or unknown id.
  - `GET /api/v1/videos/{id}/download` → 200 zip when `DONE`; 409 when not
    `DONE`; 404 for another user's video.
  - `GET /healthz`, `GET /readyz` → 200 when dependencies are up.
  - States: `PENDING → PROCESSING → DONE | FAILED`.

**1.2 Harness**
- [x] Switch the harness to run against the full stack: `BASE_URL` for the API
      and `MAILHOG_URL` for the mail inbox; `TestMain` runs
      `docker compose up -d --build --wait` / `down -v` (`KEEP_STACK=1` keeps
      it) when `BASE_URL` is not set and `deploy/docker-compose.yml` exists,
      then waits for `GET /healthz`.
- [x] Keep: ffmpeg requirement, `notImplemented(t)`. (Video generation and
      ZIP/PNG assertions come back in 1.3 with the tests that use them.)
- [x] Remove helpers that depend on the reference app's filesystem
      (`requireReferenceApp`, `resetWorkspace`, `replaceDirWithFile`, …) and
      COVERAGE_OUT support (coverage now comes from unit tests).
- [x] Delete the legacy tests (`index`, `upload`, `download`, `status`,
      `static`, `cors`, `startup`, `e2e`): they cannot compile without the old
      harness. Startup/graceful-shutdown checks move to per-service unit tests
      in Phase 2. *(Moved here from 1.4.)*
- [x] `health_test.go` — `/healthz` and `/readyz` (first test of the new suite).

**1.3 Test files (each test starts with `notImplemented(t)`)**

Helpers are added together with the first test that uses them (the `unused`
linter rejects dead helpers): `registerAndLogin(t)` (unique user per test),
authenticated client, `uploadVideos(t, token, files...)`, `makeVideo`,
`waitForStatus(t, token, id, want, timeout)` (polling), `mailsFor(t, email)`,
error-envelope decoding (done in 1.3a: `api_test.go`, `helpers_test.go`). Split in two PRs: **1.3a** auth, upload, processing,
status, download; **1.3b** concurrency, resilience, notification, e2e.

- [x] `auth_test.go` — register, duplicate, invalid input, login ok/wrong
      password, protected routes return 401 without/with invalid/expired token.
- [x] `upload_test.go` — 202 + PENDING job, several files in one request,
      missing field, every unsupported extension, every supported format
      accepted (any case).
- [x] `processing_test.go` — job reaches DONE; frame count follows video
      duration (1 fps); zip holds `frame_0001.png…` valid PNGs; corrupt video,
      audio-only file and zero-frame video reach FAILED with `error_message`.
- [x] `status_test.go` — list shows only the caller's videos, newest first,
      pagination; get-by-id; 404 for another user's id.
- [x] `download_test.go` — valid zip when DONE, 409 while PENDING/PROCESSING,
      404 for another user / unknown id.
- [x] `concurrency_test.go` — **RF1**: N videos uploaded together are
      processed in parallel (e.g. total time < sum of individual times, or
      several PROCESSING at once).
- [x] `resilience_test.go` — **RF2**: burst of M concurrent uploads, every one
      accepted and eventually DONE (none lost); optional: stop/restart a worker
      mid-run (`docker compose restart worker`) and nothing is lost.
- [x] `notification_test.go` — **RF5**: a failed video produces an e-mail to
      the owner (MailHog API) naming the video and the error; a successful one
      does not send a failure mail.
- [x] `e2e_test.go` — register → login → upload several → poll → list →
      download → failure mail.

**1.4 Clean up**
- [x] `make tools`: build golangci-lint with the module's Go toolchain
      (`GOTOOLCHAIN=go1.27.x`); a plain `go install` may pick an older Go that
      cannot lint a Go 1.27 module.
- [x] Drop the dead coverage plumbing: `make cover`, the CI coverage steps,
      and the `COVERAGE_OUT` mention in `.golangci.yml`.
- [x] Rewrite `tests/integration/README.md` (new contract, how to run against
      compose, status of pending tests) and drop the legacy "Contract notes".
- [x] CI still green: gofmt, vet, golangci-lint, and the suite with everything skipped.

### Phase 2 — Implement the new behavior (until every test is enabled)

Follow the **Ground rules** above. In short: implement against the tests
without changing them; in the same PR, delete the `notImplemented(t)` line of
every test it makes pass and tick the items here. If a test contradicts the
challenge PDF, fix the test first in a separate PR (Ground rule 2).

**2.1 Foundation**
- [x] `deploy/docker-compose.yml` with the infra the tests need: postgres,
      redis, rabbitmq (management), storage (SeaweedFS S3, ADR 0003), mailhog,
      plus the `api` service (worker/notifier join in 2.3/2.5). `.env.example`, no secrets
      committed. Note: once this file exists, `go test ./...` (and CI) starts
      the stack with `docker compose up --build --wait` and waits for
      `/healthz`, so the api service must exist and be healthy from this PR
      on. The stack must provide ≥2 processing slots, a `worker` service,
      API on :8080 and MailHog on :8025 (see `tests/integration/README.md`).
- [x] Domain package: `User`, `Video`/`Job`, `JobStatus`, supported-format
      validation.
- [x] Ports + adapters: `FrameExtractor` (ffmpeg, context + timeout, stderr
      capture), `Archiver` (zip), `ObjectStorage` (S3), readiness probes for
      Postgres and RabbitMQ, `/healthz` + `/readyz`, config, logging.
      Repositories, publisher/consumer and mailer arrive with the steps that
      use them (2.2, 2.3, 2.5).
- [x] Unit tests for domain and adapters (table-driven, fakes for ports).
- [x] Enables: `health_test.go`.

**2.2 Persistence and authentication (RF3, RT1)**
- [x] `db/migrations/` with versioned SQL (goose v3, embedded; run by the
      one-shot `migrate` compose service / `make migrate`, see
      [`docs/database.md`](../docs/database.md)):
      `users (id, email, name, password_hash, created_at)`,
      `videos (id, user_id, original_name, storage_key, zip_key, status,
      frame_count, error_message, created_at, updated_at)`, indexes on
      `(user_id, created_at)` and `status`. → **D2**
- [x] Repository layer (pgx) with integration tests against real Postgres (`POSTGRES_TEST_URL`).
- [x] Register/login (bcrypt cost 12, HS256 JWT with expiry; `JWT_SECRET` ≥ 32 bytes from env).
- [x] Auth middleware on all `/api/v1/videos*` routes.
- [x] Ownership checks on every video route (list/get done; upload/download stubs return 501 until 2.3/2.4).
- [x] Enables: `auth_test.go`.

**2.3 Messaging and async processing (RF1, RF2, RT2)**
- [x] RabbitMQ topology as code: exchange `videos`, queues `video.process`,
      `video.notify`, DLX + DLQ, durable queues, persistent messages. → **D2**
- [x] Storage bucket bootstrap (SeaweedFS `-bucket` + API `EnsureBucket`, done in 2.1; document in D2). → **D2**
- [x] `POST /api/v1/videos`: stream to storage, insert job `PENDING`, publish
      with publisher confirms, return 202.
- [x] Outbox pattern *or* publish-then-commit with reconciliation, so no job is
      stuck if the broker is down (document the choice in an ADR).
- [x] Worker service: prefetch = concurrency, manual ack, `PROCESSING` →
      `DONE`/`FAILED`, retries with backoff, DLQ after N attempts, idempotency.
- [x] Worker concurrency configurable (goroutine pool) + horizontal replicas
      (`docker compose up --scale worker=3`).
- [x] Temp files cleaned on success and failure; ffmpeg timeout.
- [x] Graceful shutdown in every service (finish/requeue in-flight messages),
      with unit tests.
- [x] Enables: `upload_test.go`, `processing_test.go`,
      `concurrency_test.go`, `resilience_test.go` — and `status_test.go`,
      `download_test.go`, since download landed here too (processing tests
      check zips through it). Design: [ADR 0004](../docs/adr/0004-transactional-outbox.md),
      [`docs/messaging.md`](../docs/messaging.md); compose runs 2 worker
      replicas × concurrency 2.

**2.4 Status listing and download (RF4)**
- [x] `GET /api/v1/videos` (paginated), `GET /api/v1/videos/{id}` (done early in 2.2: the auth tests need them).
- [x] `GET /api/v1/videos/{id}/download` (streamed; done in 2.3).
- [x] Redis cache for the list: per-user version key bumped on every change (api + worker), best-effort with Postgres fallback ([`docs/cache.md`](../docs/cache.md)).
- [x] Simple web UI at `GET /` (embedded; register/login, upload, status table with polling, download).
- [x] Enables: `status_test.go`, `download_test.go` (enabled in 2.3).

**2.5 Notifications (RF5)**
- [x] Worker publishes `video.failed` (and `video.processed`) events.
- [x] Notifier service consumes and sends e-mail via SMTP (MailHog in compose,
      real SMTP via env in prod); templated message with video name and reason.
- [x] Retry + DLQ for notification failures; unit tests with a fake mailer.
- [x] Enables: `notification_test.go`, `e2e_test.go`.

- [x] **Exit check:** `grep -rn 'notImplemented(t)$' tests/integration/` is
      empty and CI is green (35/35 integration tests pass against the compose
      stack). The `notImplemented` helper stays for future tests (lint exclusion).

### Phase 3 — Observability
- [x] Structured logs (`log/slog`, JSON) with request/job correlation id: `X-Request-ID` → outbox `correlation_id` → AMQP `correlation-id` → worker and notifier logs ([`docs/observability.md`](../docs/observability.md)).
- [ ] `/metrics` (Prometheus) on each service: HTTP latency/count, jobs
      processed/failed, processing duration, queue depth (RabbitMQ exporter).
- [ ] Prometheus + Grafana in compose with a provisioned dashboard.

### Phase 4 — Containers and infrastructure (RT2)
- [ ] Multi-stage Dockerfile per service (worker image includes ffmpeg),
      non-root user.
- [ ] Add api, worker, notifier, prometheus and grafana to `docker-compose.yml`.
- [ ] Kubernetes manifests (or Helm/Kustomize) in `deploy/k8s/`: Deployments,
      Services, ConfigMaps, Secrets, HPA for api and worker (KEDA on queue
      length as a stretch goal).
- [ ] Load test (k6 or vegeta) proving no lost requests during a spike;
      save results in `docs/`. → evidence for **RF2**

### Phase 5 — Quality (RT4)
- [ ] Coverage report in CI; target ≥ 80% on domain/use cases.
- [ ] Static analysis: `golangci-lint`, `govulncheck`; optional SonarCloud.
- [ ] Consider quorum queues with `x-delivery-limit` for `video.process`: a
      job that crashes the worker every time (e.g. OOM) is redelivered forever
      on a classic queue (found in 2.3).

### Phase 6 — CI/CD (RT5)
- [ ] CI: lint, vet, unit tests, then `docker compose up` and the integration
      suite on every PR (matrix per service).
- [ ] Build and push images to GHCR on merge to `main` (tag = commit SHA +
      `latest`).
- [ ] CD: deploy job (to a k8s cluster, or compose on a VM) triggered after
      images are pushed; document required secrets.
- [ ] Branch protection on `main` requiring CI to pass.

### Phase 7 — Documentation and delivery (D1–D4)
- [ ] `README.md`: overview, how to run locally in one command, how to test,
      API examples (curl), env vars.
- [ ] `docs/architecture.md`: context + container diagrams (C4 / Mermaid),
      sequence diagrams (upload, processing, failure notification), data model.
      → **D1**
- [ ] `docs/adr/` with the decisions from §4 and §5.
- [ ] DB and resource scripts referenced from the README (migrations, RabbitMQ
      definitions, storage bucket). → **D2**
- [ ] Final GitHub repository link(s) collected for submission. → **D3**
- [ ] Video script/outline (≤ 10 min): docs → architecture → live demo
      (multiple uploads, scaling workers, status list, download, failure
      e-mail, Grafana dashboard, CI run). → **D4**
- [ ] Record and upload the video; add the link to the README. → **D4**

---

## 7. Requirement traceability

| Req | Test (Phase 1) | Implementation | Done |
|---|---|---|---|
| RF1 Parallel processing | `concurrency_test.go` | 2.3 (queue + worker pool + replicas) | [x] |
| RF2 No lost requests on peaks | `resilience_test.go` | 2.3 (durable queue, confirms, ack, DLQ, outbox) + Phase 4 load test | [ ] tests pass; load test pending |
| RF3 User/password protection | `auth_test.go` | 2.2 (register/login, JWT, ownership checks) | [x] |
| RF4 Status listing per user | `status_test.go`, `download_test.go` | 2.2–2.4 | [x] |
| RF5 Error notification | `notification_test.go` | 2.5 | [x] |
| RT1 Persistence | all (state survives across requests) | 2.2 (Postgres) + 2.3 (object storage) | [x] |
| RT2 Scalable | `concurrency_test.go` | 2.3 + Phase 4 (stateless services, compose scale, k8s HPA) | [ ] |
| RT3 GitHub versioning | — | Repo exists; PR-based flow | [x] |
| RT4 Tests | Phase 1 suite | Phase 2 unit tests + Phase 5 | [ ] |
| RT5 CI/CD | — | Phase 6 (CI exists, CD missing) | [ ] |
| D1 Architecture docs | — | Phase 7 | [ ] |
| D2 DB/resource scripts | — | 2.2, 2.3, Phase 7 | [ ] |
| D3 GitHub link | — | Phase 7 | [ ] |
| D4 ≤ 10-min video | — | Phase 7 | [ ] |

## 8. Progress log

| Date | Change |
|---|---|
| 2026-09-24 | Plan created; challenge PDF added to `.ai-agents/`. |
| 2026-09-24 | §5 decided: Phase 1 rebuilds the tests for the new behavior, Phase 2 implements it. Phases renumbered. |
| 2026-09-24 | Added Ground rules: tests are the spec (PDF wins on conflict), avoid changing them, enable them with the feature. |
| 2026-09-24 | Phase 0 done: ADRs 0001/0002 + CLAUDE.md (#10), Go 1.27 (#11), Makefile + .editorconfig (#12), golangci-lint v2.14.0 (#13), `__MACOSX` cleanup. |
| 2026-09-24 | Phase 1 done: OpenAPI v1 contract (#15), stack harness + legacy tests removed (#16), 35 v1 tests all skipped (#17, #18; validated against a throwaway fake), README/Makefile/CI cleanup. |
| 2026-09-24 | Phase 2 done: foundation + compose (#20), persistence/auth (#21), outbox/worker/download (#22), Redis cache + web UI (#23), notifications (2.5). All 35 integration tests enabled and passing. |
