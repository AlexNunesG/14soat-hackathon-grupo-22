# FIAP X Video Processor

Repository: https://github.com/14SOAT-HACKATHON/app

SOAT Phase 5 hackathon project. Users register, upload one or more videos,
and later download a `.zip` of their frames (extracted at 1 frame per
second). The legacy single-process demo has been rebuilt as three Go
microservices — **api**, **worker**, **notifier** — talking to PostgreSQL,
Redis, RabbitMQ, S3-compatible object storage (SeaweedFS locally, see
[ADR 0003](docs/adr/0003-object-storage-seaweedfs.md)), MailHog and
Prometheus/Grafana. Uploads are accepted immediately and processed
asynchronously by a pool of workers, so more than one video is processed at
a time and a load spike never loses a request.

The requirements this satisfies (RF1–RF5, RT1–RT5) and the deliverables
(D1–D4) are listed and tracked in
[`.ai-agents/PLAN.md`](.ai-agents/PLAN.md#2-requirements-from-the-pdf); the
current status of each is in its
[requirement traceability table](.ai-agents/PLAN.md#7-requirement-traceability),
the authoritative record — not duplicated here.

## Architecture at a glance

Browser/CLI → **api** (JWT auth, upload, list, download) → PostgreSQL
(jobs) + object storage (video/zip bytes) → RabbitMQ (`video.uploaded`) →
**worker** pool (ffmpeg → PNG frames → zip) → RabbitMQ
(`video.failed`/`video.processed`) → **notifier** (SMTP e-mail on failure).
Every service exposes `/metrics`; Prometheus scrapes them and Grafana
dashboards them. See [`docs/architecture.md`](docs/architecture.md) for the
full context/container/sequence diagrams and data model, and
[`docs/adr/`](docs/adr/README.md) for the individual decisions (repo
layout, object storage, outbox, queue types) and why each was made.

## Run it locally in one command

Prerequisites: Docker + Docker Compose. No other setup is required —
[`deploy/docker-compose.yml`](deploy/docker-compose.yml) has working
defaults for every variable. Copying [`.env.example`](.env.example) to a
`.env` is **optional**, only needed to override a default (see
[Environment variables](#environment-variables)).

```sh
make up
```

This builds and starts the whole stack and waits until every service is
healthy: `api` (:8080), `worker` (2 replicas), `notifier`, PostgreSQL
(:5432), Redis (:6379), RabbitMQ (:5672, management UI :15672), object
storage/SeaweedFS (:8333), MailHog (:8025), Prometheus (:9091) and Grafana
(:3000).

Open the web UI at <http://localhost:8080/> to register, upload and watch
videos move through `PENDING → PROCESSING → DONE`/`FAILED` from a browser.

Tear everything down (containers and volumes):

```sh
make down
```

## API examples (curl)

The full contract — routes, payloads, status/error codes — is
[`docs/openapi.yaml`](docs/openapi.yaml). With the stack up (`make up`):

```sh
# 1. Register
curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada Lovelace","email":"ada@example.com","password":"correct-horse-battery"}'

# 2. Login, capture the bearer token into a shell variable
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"ada@example.com","password":"correct-horse-battery"}' | jq -r .access_token)

# 3. Upload one or more videos (multipart field "videos", repeatable)
curl -s -X POST http://localhost:8080/api/v1/videos \
  -H "Authorization: Bearer $TOKEN" \
  -F videos=@holiday.mp4 \
  -F videos=@trip.mkv

# 4. List the caller's videos (paginated, newest first)
curl -s http://localhost:8080/api/v1/videos \
  -H "Authorization: Bearer $TOKEN"

# 5. Get one video by id (use an id from step 3 or 4)
VIDEO_ID=<id-from-step-3>
curl -s http://localhost:8080/api/v1/videos/$VIDEO_ID \
  -H "Authorization: Bearer $TOKEN"

# 6. Download its frames once status is DONE
curl -s -o frames.zip http://localhost:8080/api/v1/videos/$VIDEO_ID/download \
  -H "Authorization: Bearer $TOKEN"
```

`jq` is only used above to pull `access_token` out of the login response;
without it, copy the token from the raw JSON by hand. Download returns
`409 video_not_ready` while the video is `PENDING`/`PROCESSING` and
`FAILED`; poll step 5 until `status` is `DONE`.

## How to test

```sh
make check          # everything CI runs (see below); run before pushing
make test-integration  # the black-box suite (tests/integration/), verbose
make coverage        # internal/domain + internal/app unit coverage, >= 80%
make loadtest         # k6 spike test proving no request is lost under load (RF2)
make obs-check        # promtool validation of the Prometheus config/alerts
make k8s-check         # kubectl kustomize render/validation of deploy/k8s
```

`make check` runs, in order: `gofmt -l .`, `go vet ./...`,
`golangci-lint run ./...`, `make obs-check`, `make k8s-check`,
`go test -race ./...` (needs `ffmpeg` in `PATH`; this also builds and runs
the full compose stack for `tests/integration/`), `make coverage` and
`make vulncheck`. See [`docs/quality.md`](docs/quality.md) for the coverage
target and how CI enforces it, and
[`docs/loadtest/README.md`](docs/loadtest/README.md) for the load-test
results that evidence RF2 ("never lose a request during load peaks").

## Environment variables

[`.env.example`](.env.example) is the source of truth — every variable is
documented there with its default and which service reads it. Categories:

- **Compose stack**: PostgreSQL/RabbitMQ credentials, host ports,
  Prometheus/Grafana settings, `GOPROXY`, image `VERSION`.
- **api**: `HTTP_ADDR`, `METRICS_ADDR`, `DATABASE_URL`, `AMQP_URL`,
  `S3_*`, `JWT_SECRET`/`JWT_TTL`, `MAX_UPLOAD_BYTES`, outbox settings,
  `REDIS_URL`/`CACHE_TTL`.
- **worker**: `HEALTH_ADDR`, `WORKER_CONCURRENCY`/`WORKER_REPLICAS`,
  `WORKER_MAX_ATTEMPTS`, `FFMPEG_TIMEOUT`, shutdown/temp-dir settings.
- **notifier**: `NOTIFIER_CONCURRENCY`/`NOTIFIER_MAX_ATTEMPTS`, `APP_URL`,
  SMTP host/port/credentials/TLS (MailHog locally).
- **Observability**: log level, Prometheus/Grafana ports and admin
  credentials.

Nothing in `.env.example` is a real credential; the compose stack works
with no `.env` file at all.

## Database and other resource scripts (D2)

- **Database**: [`db/migrations/`](db/migrations/) (versioned SQL, goose
  v3), applied with `make migrate` or automatically by the compose stack's
  one-shot `migrate` service before `api` starts. Schema and design in
  [`docs/database.md`](docs/database.md).
- **Message broker**: [`deploy/rabbitmq/definitions.json`](deploy/rabbitmq/definitions.json)
  (topology as data; the topology itself is declared as code and applies
  automatically). Details in [`docs/messaging.md`](docs/messaging.md).
- **Object storage bucket bootstrap**: created by SeaweedFS at startup and
  idempotently ensured by the api on boot — see
  [ADR 0003](docs/adr/0003-object-storage-seaweedfs.md).

## Kubernetes / production deployment

Kustomize manifests (`base` + `dev`/`prod` overlays, HPA, an optional KEDA
autoscaler on queue depth) live in
[`deploy/k8s/`](deploy/k8s/README.md). CI/CD — GitHub Actions building and
pushing images to GHCR on every merge to `main`, then deploying to a
Kubernetes cluster (or, as an alternative, `docker compose` over SSH to a
VM) — is documented in [`docs/deployment.md`](docs/deployment.md).

## Observability

Every service logs structured JSON with a request/job correlation id and
exposes Prometheus metrics; the compose stack ships Prometheus (alert
rules included) and a provisioned Grafana dashboard covering throughput,
processing, notifications and cache. Open Prometheus at
<http://localhost:9091> and Grafana at <http://localhost:3000> after
`make up`. Full details, metric names, alert rules and a live demo script
in [`docs/observability.md`](docs/observability.md).

## Project structure

```
cmd/{api,worker,notifier}   entrypoints (wiring only)
internal/domain             entities, JobStatus, format validation
internal/app                use cases and ports
internal/adapters/          http (+ web UI), postgres, rabbitmq, redis,
                             storage, auth, ffmpeg, zip, mailer, prom
internal/platform/          config, logging, metrics
db/migrations/              versioned SQL (the DB creation script)
deploy/                     compose, Dockerfiles, k8s, RabbitMQ topology,
                             Prometheus/Grafana provisioning, load tests
docs/                       architecture, ADRs, openapi.yaml, per-topic docs
tests/integration/          black-box suite — the executable spec
.ai-agents/PLAN.md          living plan, checklist and traceability table
```

Look in `internal/domain`/`internal/app` for business rules, `internal/adapters`
for how a rule talks to Postgres/RabbitMQ/S3/SMTP, `tests/integration` for
what the system is contractually required to do, and `docs/` for why it is
built this way.

## Documentation index

| Doc | Covers |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Context/container/sequence diagrams, data model (D1) |
| [`docs/adr/`](docs/adr/README.md) | Architecture decision records (repo layout, object storage, outbox, queue types, …) |
| [`docs/openapi.yaml`](docs/openapi.yaml) | v1 HTTP contract — routes, payloads, status/error codes |
| [`docs/database.md`](docs/database.md) | Schema, migrations, how they run |
| [`docs/messaging.md`](docs/messaging.md) | RabbitMQ topology, retries, dead-lettering, idempotency |
| [`docs/cache.md`](docs/cache.md) | Redis cache of the video list |
| [`docs/notifications.md`](docs/notifications.md) | Failure e-mail flow, retries, dedup |
| [`docs/observability.md`](docs/observability.md) | Logs, metrics, Prometheus alerts, Grafana dashboard |
| [`docs/deployment.md`](docs/deployment.md) | Image publishing (GHCR), CI/CD, Kubernetes/VM deployment |
| [`docs/quality.md`](docs/quality.md) | Coverage target and vulnerability scanning gates |
| [`docs/loadtest/README.md`](docs/loadtest/README.md) | k6 spike test and results evidencing RF2 |
| [`deploy/k8s/README.md`](deploy/k8s/README.md) | Kubernetes manifests, config/secrets, applying them |
| [`tests/integration/README.md`](tests/integration/README.md) | The integration suite, ground rules, how to run it |
| [`.ai-agents/PLAN.md`](.ai-agents/PLAN.md) | Living plan, checklist and requirement traceability table |

## Status

What's actually done, requirement by requirement, is tracked in
[`.ai-agents/PLAN.md`'s requirement traceability table](.ai-agents/PLAN.md#7-requirement-traceability)
— treat that table, not this README, as the up-to-date record.
