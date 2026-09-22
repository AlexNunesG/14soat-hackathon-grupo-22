# FIAP X — Video Processing System

Microservices rewrite of the FIAP X hackathon video-processing prototype:
username/password auth, async video upload → ffmpeg frame extraction via a
queue-backed worker pool, per-user status tracking, and failure email
notifications — built for concurrent processing and horizontal scale.

The full target architecture, data model, message contracts, and the
phased delivery plan this repo is being built against live in
[`.ai-agents/PLAN.md`](.ai-agents/PLAN.md). This README stays a short
quickstart; see that doc for the roadmap and `docs/ARCHITECTURE.md`
(added in a later delivery) for the in-depth write-up.

## Repo layout

```
cmd/{auth-service,video-service,worker-service,notification-service}/main.go
internal/{auth,authmw,videos,users,mq,storage,notify,config,db,metrics}/
infra/docker-compose.yml         # Postgres, RabbitMQ, MinIO, Mailpit, Prometheus, Grafana
infra/postgres/init/             # DB creation script(s), auto-applied on first boot
infra/prometheus/, infra/grafana/  # monitoring config
.github/workflows/ci.yml
docs/
```

A single Go module (`go.mod`) covers the whole monorepo — no `go.work`.

## Status

This repo is currently at **Delivery 0 — repo & infra skeleton**: the
folder layout, Go module, and backing infra (Postgres/RabbitMQ/MinIO/
Mailpit/Prometheus/Grafana) are wired up in Compose. The 4 `cmd/*/main.go`
entrypoints are placeholder stubs — no auth, video, worker, or notification
logic yet. That lands in Deliveries 1-4 per the plan above.

## Quickstart — infra-only stack

Brings up Postgres, RabbitMQ, MinIO, Mailpit, Prometheus and Grafana (no
app services yet):

```bash
docker compose -f infra/docker-compose.yml up -d
docker compose -f infra/docker-compose.yml ps   # wait for all to be healthy
```

Once healthy:

| Service    | URL                              | Notes                          |
|------------|-----------------------------------|---------------------------------|
| Postgres   | `localhost:5432`                  | user/pass/db default to `fiapx` |
| RabbitMQ   | http://localhost:15672            | management UI, `fiapx`/`fiapx`  |
| MinIO      | http://localhost:9001             | console, `fiapx`/`fiapx123`     |
| Mailpit    | http://localhost:8025             | dev SMTP inbox                  |
| Prometheus | http://localhost:9090             | placeholder config for now      |
| Grafana    | http://localhost:3000             | `admin`/`admin`, Prometheus datasource pre-provisioned |

Tear down (including volumes):

```bash
docker compose -f infra/docker-compose.yml down -v
```

## Building the Go module

```bash
go build ./...
```

## Roadmap

See the Deliveries checklist in [`.ai-agents/PLAN.md`](.ai-agents/PLAN.md)
for what's next (auth-service, video-service, worker-service,
notification-service, integration tests, CI/CD, monitoring, docs).
