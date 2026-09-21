# FIAP X — Video Processing System: Microservices Rewrite

## Context

The repo currently contains only the "before" project handed out for the hackathon: a single `main.go` (Gin) that synchronously accepts a video upload, shells out to `ffmpeg` to extract 1 frame/sec, zips the frames, and returns everything in one HTTP response. There is no auth, no persistence, no queue, no per-user concept, and the `Dockerfile` is explicitly labeled "sem boas práticas." There's also stray `__MACOSX/` zip-extraction junk in the repo.

The hackathon brief asks for a full architectural rewrite that demonstrates: concurrent multi-video processing, no lost requests under load spikes, username/password auth, per-user status listing, error notifications, persistence, horizontal scalability, tests, and CI/CD — plus a written architecture doc, a DB creation script, the GitHub link, and a ≤10-minute demo video. This is a from-scratch build (no legacy coupling to preserve), so the plan below designs the target system and a phased path to it, with every phase demoable.

Stack decisions already confirmed: **Docker Compose** (no Kubernetes), **RabbitMQ**, **MinIO** (S3-compatible object storage, not a shared volume — needed because API and worker replicas must scale independently), **PostgreSQL** (+ Redis available if a caching need arises), **Prometheus + Grafana**, **GitHub Actions**, all services in **Go** (matches the base project, single monorepo/module).

## Architecture

**Four services, one shared internal library, no gateway container:**

- **`auth-service`** — register/login, bcrypt password hashing, issues HS256 JWTs (shared secret with video-service — same trust boundary, so no JWKS/network-call-per-request needed).
- **`video-service`** — the only public video API. `POST /videos` streams the upload straight to MinIO, inserts a Postgres row (`PENDING`), publishes `video.processing.requested` to RabbitMQ **with publisher confirms**, and returns `202` immediately (never waits for ffmpeg — this is the crux of "don't lose requests under load"). Also `GET /videos` (list own, by JWT `sub`), `GET /videos/{id}`, `GET /videos/{id}/download` (presigned MinIO URL).
- **`worker-service`** — consumes `video.processing.requested` with **manual ack** and `prefetch_count=1`, runs ffmpeg, zips frames, uploads to MinIO, updates Postgres to `COMPLETED`/`FAILED`. This is the service scaled horizontally (`docker compose up --scale worker-service=N`) to satisfy "process more than one video at a time." If a worker dies mid-job, the unacked message is redelivered to another replica — demoed live via `docker compose kill worker`.
- **`notification-service`** — separate consumer of `video.processing.failed`, sends email via **Mailpit** (SMTP dev-mail, web UI at `:8025`, no real provider needed for the demo). Kept as its own service rather than a worker package so SMTP flakiness/retries can't block ffmpeg processing.

`video-service` and `worker-service` share domain code via `internal/videos` (model + Postgres repository) and `internal/mq` (message contracts) — no duplicated schema/struct mapping, but still independently deployable/scalable.

**Reliability mechanics that satisfy "no lost requests under load":** durable exchange/queues + persistent messages + publisher confirms on the producer side; manual ack only after DB update on the consumer side; dead-letter exchange + per-queue DLQ after `retry_count >= 3` (tracked in Postgres) so a permanently-broken video doesn't loop forever, with the worker publishing `video.processing.failed` itself when it gives up. RabbitMQ's own queue depth absorbs spikes — video-service stays fast (upload+insert+publish only), so HTTP never blocks on processing capacity.

**Data model** (`infra/postgres/init/001_init.sql`, auto-applied via Compose `docker-entrypoint-initdb.d`):

```sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           VARCHAR(255) NOT NULL UNIQUE,
    password_hash   VARCHAR(255) NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE video_status AS ENUM ('PENDING','PROCESSING','COMPLETED','FAILED');

CREATE TABLE videos (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    original_filename   VARCHAR(500) NOT NULL,
    status              video_status NOT NULL DEFAULT 'PENDING',
    storage_input_key   VARCHAR(1000) NOT NULL,
    storage_output_key  VARCHAR(1000),
    frame_count         INTEGER,
    error_message       TEXT,
    retry_count         INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ
);
CREATE INDEX idx_videos_user_id ON videos(user_id);
CREATE INDEX idx_videos_status ON videos(status);
```

**Message contracts** (RabbitMQ topic exchange `video.events`, durable, persistent messages):
- `video.processing.requested` (video-service → worker-service): `{video_id, user_id, storage_input_key, original_filename}`
- `video.processing.completed` (worker-service → informational, DB is source of truth): `{video_id, storage_output_key, frame_count}`
- `video.processing.failed` (worker-service → notification-service): `{video_id, user_id, user_email, error_message, retry_count}`

Each envelope also carries `message_id`, `video_id`, `user_id`, `occurred_at`. Each work/failure queue gets its own DLX/DLQ.

**Repo layout:**
```
cmd/{auth-service,video-service,worker-service,notification-service}/main.go
internal/{auth,authmw,videos,users,mq,storage,notify,config,db,metrics}/
infra/{docker-compose.yml, postgres/init/001_init.sql, prometheus/prometheus.yml, grafana/provisioning+dashboards}
Dockerfile.auth-service / Dockerfile.video-service / Dockerfile.worker-service / Dockerfile.notification-service
.github/workflows/ci.yml
docs/ARCHITECTURE.md
```
Only `worker-service`'s Dockerfile installs ffmpeg. Single Go module for the whole monorepo (no `go.work` needed at this scale).

**Testing:** unit tests (stdlib `testing` + `testify`) per service against interfaces (mocked repo/publisher/exec runner) — JWT round-trip, bcrypt, message (de)serialization, handler behavior, ffmpeg-wrapper error capture. Integration tests (`testcontainers-go`, build-tagged `integration`) spin up real Postgres/RabbitMQ/MinIO to verify: upload→row→queued end-to-end, full pipeline consume→ffmpeg→MinIO→`COMPLETED`, failure→`FAILED`+DLQ+notification message, and auth register/login/JWT-middleware accept-reject.

**CI/CD** (`.github/workflows/ci.yml`, one workflow): `lint` (golangci-lint) → `unit-test` (`go test ./...`) → `integration-test` (`go test -tags=integration ./...`, using testcontainers against the runner's built-in Docker) → `build-and-push` (matrix over the 4 services, `docker/build-push-action` to GHCR, tagged `latest` + `${{ github.sha }}`), gated on `main` and on the prior jobs passing.

**Monitoring:** each service exposes `/metrics` (prometheus/client_golang) — HTTP latency/count on auth/video-service; `video_processing_duration_seconds`, `videos_processed_total{result}`, `video_processing_in_progress` (gauge, sums across worker replicas — direct evidence of concurrency) on worker-service; `emails_sent_total{result}` on notification-service. RabbitMQ's built-in `rabbitmq_prometheus` plugin is scraped directly for real queue-depth metrics rather than reimplementing them. Grafana auto-provisions one dashboard (HTTP row, queue-depth row, processing row, notifications row) via `infra/grafana/provisioning/` so `docker compose up` gives a working dashboard with no manual setup.

## Requirements Explicitly Addressed (grading checklist)

- Concurrent multi-video processing → worker replicas + RabbitMQ fair dispatch (`prefetch_count=1`).
- No lost requests under spikes → durable queue + publisher confirms + manual ack + DLQ/retry.
- Username/password protection → bcrypt-hashed passwords, HS256 JWT, Gin middleware on video-service.
- Per-user status listing → `GET /videos` filtered by JWT `sub`.
- Error notification → notification-service + Mailpit (swap for SES/SendGrid in production — call this out explicitly in the doc).
- Persistence → PostgreSQL (users, videos/jobs).
- Horizontal scalability → stateless services + MinIO (not local disk) + `docker compose --scale`, demoed live.
- Tests → unit + testcontainers integration suites.
- CI/CD → GitHub Actions (lint/test/integration/build-push).
- File-size cap on upload (`MAX_UPLOAD_SIZE_MB`) so a single huge upload can't undermine the load-spike demo.
- Worker does an idempotency check (`if status == COMPLETED: ack+skip`) guarding against rare double-delivery.

## Final Verification

- `go test ./...` (unit) and `go test -tags=integration ./...` (testcontainers) passing locally and in CI.
- `docker compose up` brings up all infra (Postgres, RabbitMQ, MinIO, Prometheus, Grafana, Mailpit) + all 4 app services healthy; Grafana dashboard populated after a load test; RabbitMQ management UI (`:15672`) and MinIO console reachable.
- End-to-end manual pass: register → login → upload video → poll `GET /videos` until `COMPLETED` → download via presigned URL → unzip and confirm frames extracted at 1fps, matching current behavior.
- Failure path: upload a non-video/corrupt file → status becomes `FAILED` with `error_message` → email appears in Mailpit.
- Scale + kill test: `docker compose up --scale worker-service=3`, upload multiple videos, `docker compose kill` one worker mid-job, confirm the video still completes via redelivery.

---

## Deliveries Checklist

Each item below is meant to be a small, independently committable/demoable unit of work. Check items off as they land.

### Delivery 0 — Repo & infra skeleton

- [ ] Create monorepo folder structure (`cmd/`, `internal/`, `infra/`, `docs/`)
- [ ] Remove `__MACOSX/` and legacy flat `uploads/`, `outputs/`, `temp/` dirs and old `main.go`/`Dockerfile`
- [ ] Initialize single Go module for the monorepo (`go.mod`)
- [ ] `infra/docker-compose.yml`: PostgreSQL service + healthcheck
- [ ] `infra/docker-compose.yml`: RabbitMQ service (management image + Prometheus plugin enabled) + healthcheck
- [ ] `infra/docker-compose.yml`: MinIO service + healthcheck
- [ ] `infra/docker-compose.yml`: Mailpit service
- [ ] `infra/docker-compose.yml`: Prometheus service (placeholder config)
- [ ] `infra/docker-compose.yml`: Grafana service (placeholder provisioning)
- [ ] Verify `docker compose up` brings all infra containers up healthy

### Delivery 1 — auth-service

- [ ] `internal/config`: env var loading shared by all services
- [ ] `internal/db`: Postgres connection pool (pgx/pgxpool) setup
- [ ] `internal/users`: model + Postgres repository
- [ ] `internal/auth`: bcrypt password hashing helpers
- [ ] `internal/auth`: JWT sign/verify (HS256, shared secret)
- [ ] `infra/postgres/init/001_init.sql`: `users` table (+ `pgcrypto` extension)
- [ ] `cmd/auth-service`: `POST /auth/register`
- [ ] `cmd/auth-service`: `POST /auth/login`
- [ ] `cmd/auth-service`: `GET /auth/health`
- [ ] `Dockerfile.auth-service`
- [ ] Unit tests: JWT round-trip + expiry rejection, bcrypt hash/compare
- [ ] Unit tests: register/login handlers (mocked repository)
- [ ] Wire `auth-service` into `docker-compose.yml`
- [ ] Manual demo: curl register + login, decode returned JWT

### Delivery 2 — video-service

- [ ] `internal/storage`: MinIO client wrapper (upload, presigned GET)
- [ ] `internal/videos`: model + status enum + Postgres repository
- [ ] `infra/postgres/init/001_init.sql`: `videos` table + indexes
- [ ] `internal/mq`: message contracts (structs mirroring the 3 event types)
- [ ] `internal/mq`: publisher wrapper with publisher confirms
- [ ] `internal/authmw`: Gin JWT-verification middleware
- [ ] `cmd/video-service`: `POST /videos` (stream to MinIO → insert row → publish confirmed → 202)
- [ ] `cmd/video-service`: enforce `MAX_UPLOAD_SIZE_MB` upload cap
- [ ] `cmd/video-service`: `GET /videos` (list own, paginated)
- [ ] `cmd/video-service`: `GET /videos/{id}` (404 for not-found-or-not-owned)
- [ ] `cmd/video-service`: `GET /videos/{id}/download` (presigned MinIO URL)
- [ ] `cmd/video-service`: `GET /videos/health`
- [ ] `Dockerfile.video-service`
- [ ] Unit tests: handlers with mocked repo/publisher (validation, 202 shape, list filtering, 404 behavior)
- [ ] Wire `video-service` into `docker-compose.yml`
- [ ] Manual demo: upload a video, see `PENDING` row in Postgres and message sitting in the RabbitMQ queue (no worker yet)

### Delivery 3 — worker-service

- [ ] `internal/mq`: consumer wrapper (manual ack, `prefetch_count=1`, DLX/DLQ declaration)
- [ ] ffmpeg wrapper behind an injectable exec-runner interface (unit-testable without shelling out)
- [ ] Frame-zip logic ported from the base project's `createZipFile`/`addFileToZip`
- [ ] `cmd/worker-service`: consume loop — download input from MinIO, run ffmpeg (`fps=1`), zip frames, upload result to MinIO
- [ ] Status transitions in Postgres: `PENDING → PROCESSING → COMPLETED/FAILED`, with `started_at`/`completed_at`
- [ ] Idempotency guard: skip + ack immediately if video already `COMPLETED`
- [ ] Retry-count tracking + `Nack(requeue=false)` to DLQ after `retry_count >= 3`
- [ ] Publish `video.processing.completed` / `video.processing.failed`
- [ ] `Dockerfile.worker-service` (only this image installs ffmpeg)
- [ ] Unit tests: ffmpeg-wrapper failure capture, zip logic against fixture PNGs
- [ ] Wire `worker-service` into `docker-compose.yml`
- [ ] Manual demo: `docker compose up --scale worker-service=3`, upload several videos concurrently, `docker compose kill` one worker mid-job and confirm redelivery/completion

### Delivery 4 — notification-service

- [ ] `internal/notify`: SMTP mailer targeting Mailpit
- [ ] `cmd/notification-service`: consume `video.processing.failed`, render + send failure email
- [ ] Dead-letter handling for the notification queue (SMTP-down resilience)
- [ ] `Dockerfile.notification-service`
- [ ] Unit tests: email template rendering from a fixture payload
- [ ] Wire `notification-service` into `docker-compose.yml`
- [ ] Manual demo: upload a corrupt/non-video file, see the failure email appear in the Mailpit web UI

### Delivery 5 — Automated integration tests

- [ ] `testcontainers-go` integration test: `video-service` upload → real Postgres row + real queue message
- [ ] Integration test: full pipeline — publish request → worker consumes → MinIO has zip → Postgres `COMPLETED`
- [ ] Integration test: failure path — corrupt input → Postgres `FAILED` + `error_message` + `.failed` message published
- [ ] Integration test: auth — register/login against real Postgres, JWT middleware accepts valid / rejects tampered-expired tokens
- [ ] Build-tag separation so `go test ./...` stays fast (unit only) and `go test -tags=integration ./...` runs the above

### Delivery 6 — CI/CD

- [ ] `.github/workflows/ci.yml`: `lint` job (golangci-lint)
- [ ] `unit-test` job (`go test ./... -cover`)
- [ ] `integration-test` job (testcontainers, depends on `unit-test`)
- [ ] `build-and-push` job (matrix over 4 services → GHCR, tagged `latest` + `${{ github.sha }}`, gated on `main` + prior jobs green)
- [ ] Verify pipeline runs green on a PR and on a `main` push

### Delivery 7 — Monitoring

- [ ] `internal/metrics`: shared Prometheus registration helpers
- [ ] Instrument `auth-service`/`video-service`: `http_requests_total`, `http_request_duration_seconds`
- [ ] Instrument `video-service`: `videos_uploaded_total`, `video_upload_bytes`
- [ ] Instrument `worker-service`: `video_processing_duration_seconds`, `videos_processed_total{result}`, `video_processing_in_progress`
- [ ] Instrument `notification-service`: `emails_sent_total{result}`
- [ ] `infra/prometheus/prometheus.yml`: scrape targets for all 4 services + RabbitMQ's `rabbitmq_prometheus` plugin
- [ ] `infra/grafana/provisioning`: datasource + auto-loaded dashboard JSON (HTTP row, queue-depth row, processing row, notifications row)
- [ ] Run a small load test (`k6`/`hey`, ~50 concurrent uploads) and confirm the dashboard shows a visible spike-and-drain pattern

### Delivery 8 — Documentation & presentation deliverables

- [ ] `docs/ARCHITECTURE.md`: diagram + decomposition rationale + message contracts + reliability guarantees
- [ ] Confirm `infra/postgres/init/001_init.sql` is complete and stands alone as the DB-creation-script deliverable
- [ ] `README.md`: `docker compose up` quickstart + curl/Postman examples for every endpoint
- [ ] Record ≤10-minute presentation video: documentation, architecture, live working demo (including scale + kill-worker + failure-notification moments)
