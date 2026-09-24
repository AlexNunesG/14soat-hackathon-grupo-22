# FIAP X Video Processor — Hackathon Plan (SOAT Phase 5)

Source: [`POSTECH_-_SOAT_-_Fase_5_-_Hacka.pdf`](./POSTECH_-_SOAT_-_Fase_5_-_Hacka.pdf)

This is a living checklist. Tick items (`- [x]`) in the same PR that delivers them,
and add a short note or link (PR, file) when useful. Keep the **Requirement
traceability** table in sync: a requirement is done only when every item that
maps to it is ticked.

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

- Go module `video-processor` (Go 1.21, Gin). The original `main.go` and
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
│ MinIO │       │ PostgreSQL │◀── status updates ─│  RabbitMQ  │
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
- **Object storage (MinIO/S3)** instead of local disk → stateless API/workers → RT2.
- **PostgreSQL** as source of truth for users and jobs → RT1; **Redis** for
  status-list cache and rate limiting (optional).
- **Idempotent worker**: job id as message id; skip if job already `DONE`;
  retries with backoff, then DLQ + `FAILED` status + notification.
- Job states: `PENDING → PROCESSING → DONE | FAILED`.

## 5. Open decision: the legacy integration tests

The existing suite asserts the old contract (synchronous `POST /upload`
returning the zip, no auth, public `/uploads/*`). That conflicts with RF3
(auth) and with async processing (RF1/RF2). Options:

1. **Recommended:** keep the suite as the contract for a *legacy-compatible*
   milestone (M1 below), then write a new black-box suite for the v1 API
   (`/api/v1/...`, auth, async) and retire legacy tests explicitly, one PR at a
   time, documenting why in `tests/integration/README.md`.
2. Drop the legacy contract immediately and write only the new suite.

- [ ] Team decides option 1 or 2 and records it in an ADR.

---

## 6. Checklist by phase

### Phase 0 — Project setup
- [x] Create `.ai-agents/` with the challenge PDF and this plan.
- [ ] Decide repo layout (monorepo `services/api`, `services/worker`,
      `services/notifier`, `pkg/` shared) and record it in an ADR.
- [ ] Upgrade Go version (1.21 is EOL) and pin tool versions.
- [ ] Add `Makefile` (`make lint test up down`) and `.editorconfig`.
- [ ] Add `golangci-lint` config and run it in CI.
- [ ] Remove the stray `__MACOSX/` folder and add it to `.gitignore`.

### Phase 1 (M1) — Rebuild the core processing, cleanly (optional per §5)
- [ ] Domain package: `Video`, `Job`, `JobStatus`, validation of supported
      formats (`mp4, avi, mov, mkv, wmv, flv, webm`, any case).
- [ ] `FrameExtractor` port + ffmpeg adapter (context, timeout, stderr capture).
- [ ] `Archiver` port + zip adapter.
- [ ] `Storage` port with local-disk adapter (MinIO adapter comes in Phase 3).
- [ ] HTTP layer reproducing the legacy contract; re-enable legacy tests one
      endpoint per PR (`index`, `upload`, `status`, `download`, `static`,
      `cors`, `startup`, `e2e`).
- [ ] Unit tests for domain and adapters (table-driven, fakes for ports).

### Phase 2 — Persistence and authentication (RF3, RT1)
- [ ] `db/migrations/` with versioned SQL (golang-migrate or goose):
      `users (id, email, name, password_hash, created_at)`,
      `videos/jobs (id, user_id, original_name, storage_key, zip_key, status,
      frame_count, error_message, created_at, updated_at)`, indexes on
      `(user_id, created_at)` and `status`. → **D2**
- [ ] Repository layer (pgx) with integration tests against real Postgres
      (testcontainers or compose service in CI).
- [ ] `POST /api/v1/auth/register`, `POST /api/v1/auth/login` (bcrypt, JWT with
      expiry; secret from env).
- [ ] Auth middleware on all `/api/v1/videos*` routes; 401 without/invalid token.
- [ ] Users only see and download their own videos (403/404 otherwise) — test it.

### Phase 3 — Messaging and async processing (RF1, RF2, RT2)
- [ ] RabbitMQ topology as code: exchange `videos`, queues `video.process`,
      `video.notify`, DLX + DLQ, durable queues, persistent messages.
- [ ] MinIO adapter for `Storage`; bucket bootstrap script. → **D2**
- [ ] `POST /api/v1/videos` (multipart, multiple files allowed): stream to
      storage, insert job `PENDING`, publish with publisher confirms, return
      `202 Accepted` with job ids.
- [ ] Outbox pattern *or* publish-then-commit with reconciliation, so no job is
      stuck if the broker is down (document the choice).
- [ ] Worker service: prefetch = concurrency, manual ack, `PROCESSING` →
      `DONE`/`FAILED`, retries with backoff, DLQ after N attempts, idempotency.
- [ ] Worker concurrency configurable (goroutine pool) + horizontal replicas.
- [ ] Temp files cleaned on success and failure; ffmpeg timeout.
- [ ] Graceful shutdown in every service (finish/requeue in-flight messages).
- [ ] Load test (k6 or vegeta) proving no lost requests during a spike;
      save results in `docs/`. → evidence for **RF2**

### Phase 4 — Status listing and download (RF4)
- [ ] `GET /api/v1/videos` — paginated list of the user's videos with status,
      frame count, error message, timestamps.
- [ ] `GET /api/v1/videos/{id}` — single job status.
- [ ] `GET /api/v1/videos/{id}/download` — zip stream or presigned URL; 409 if
      not `DONE`.
- [ ] Redis cache for the list (invalidate on status change) — optional but in
      the recommended stack.
- [ ] Simple web UI (login, upload, status table with polling, download).

### Phase 5 — Notifications (RF5)
- [ ] Worker publishes `video.failed` (and `video.processed`) events.
- [ ] Notifier service consumes and sends e-mail via SMTP (MailHog in compose,
      real SMTP via env in prod); templated message with video name and reason.
- [ ] Retry + DLQ for notification failures; tests with a fake mailer.

### Phase 6 — Observability
- [ ] Structured logs (`log/slog`, JSON) with request/job correlation id.
- [ ] `/metrics` (Prometheus) on each service: HTTP latency/count, jobs
      processed/failed, processing duration, queue depth (RabbitMQ exporter).
- [ ] `/healthz` (liveness) and `/readyz` (DB, broker, storage) endpoints.
- [ ] Prometheus + Grafana in compose with a provisioned dashboard.

### Phase 7 — Containers and infrastructure (RT2)
- [ ] Multi-stage Dockerfile per service (worker image includes ffmpeg),
      non-root user.
- [ ] `docker-compose.yml`: postgres, redis, rabbitmq (management), minio,
      mailhog, prometheus, grafana, api, worker (scalable:
      `docker compose up --scale worker=3`), notifier.
- [ ] `.env.example` documenting every variable; no secrets committed.
- [ ] Kubernetes manifests (or Helm/Kustomize) in `deploy/k8s/`: Deployments,
      Services, ConfigMaps, Secrets, HPA for api and worker (KEDA on queue
      length as a stretch goal).

### Phase 8 — Quality (RT4)
- [ ] Unit tests for domain, use cases and adapters.
- [ ] Integration tests with real Postgres/RabbitMQ/MinIO (testcontainers or
      compose in CI).
- [ ] New black-box E2E suite for v1: register → login → upload several videos
      → poll status → download → failure path triggers notification (checks
      MailHog API).
- [ ] Coverage report in CI; target ≥ 80% on domain/use cases.
- [ ] Static analysis: `golangci-lint`, `govulncheck`; optional SonarCloud.

### Phase 9 — CI/CD (RT5)
- [ ] CI: lint, vet, unit, integration, E2E on every PR (matrix per service).
- [ ] Build and push images to GHCR on merge to `main` (tag = commit SHA +
      `latest`).
- [ ] CD: deploy job (to a k8s cluster, or compose on a VM) triggered after
      images are pushed; document required secrets.
- [ ] Branch protection on `main` requiring CI to pass.

### Phase 10 — Documentation and delivery (D1–D4)
- [ ] `README.md`: overview, how to run locally in one command, how to test,
      API examples (curl), env vars.
- [ ] `docs/architecture.md`: context + container diagrams (C4 / Mermaid),
      sequence diagrams (upload, processing, failure notification), data model.
      → **D1**
- [ ] `docs/adr/` with the decisions from §4 and §5.
- [ ] OpenAPI spec for the v1 API (`docs/openapi.yaml`).
- [ ] DB and resource scripts referenced from the README (migrations, RabbitMQ
      definitions, MinIO bucket). → **D2**
- [ ] Final GitHub repository link(s) collected for submission. → **D3**
- [ ] Video script/outline (≤ 10 min): docs → architecture → live demo
      (multiple uploads, scaling workers, status list, download, failure
      e-mail, Grafana dashboard, CI run). → **D4**
- [ ] Record and upload the video; add the link to the README. → **D4**

---

## 7. Requirement traceability

| Req | Covered by | Done |
|---|---|---|
| RF1 Parallel processing | Phase 3 (queue + worker pool + replicas) | [ ] |
| RF2 No lost requests on peaks | Phase 3 (durable queue, confirms, ack, DLQ, outbox, load test) | [ ] |
| RF3 User/password protection | Phase 2 (register/login, JWT, ownership checks) | [ ] |
| RF4 Status listing per user | Phase 4 | [ ] |
| RF5 Error notification | Phase 5 | [ ] |
| RT1 Persistence | Phase 2 (Postgres) + Phase 3 (object storage) | [ ] |
| RT2 Scalable | Phases 3 & 7 (stateless services, compose scale, k8s HPA) | [ ] |
| RT3 GitHub versioning | Repo exists; PR-based flow | [x] |
| RT4 Tests | Phase 8 (+ existing integration suite) | [ ] |
| RT5 CI/CD | Phase 9 (CI exists, CD missing) | [ ] |
| D1 Architecture docs | Phase 10 | [ ] |
| D2 DB/resource scripts | Phases 2, 3, 10 | [ ] |
| D3 GitHub link | Phase 10 | [ ] |
| D4 ≤ 10-min video | Phase 10 | [ ] |

## 8. Progress log

| Date | Change |
|---|---|
| 2026-09-24 | Plan created; challenge PDF added to `.ai-agents/`. |
