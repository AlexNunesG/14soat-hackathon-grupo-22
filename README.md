# FIAP X — Video Processing System

Microservices rewrite of the FIAP X video-to-frames prototype, built for the FIAP X hackathon.

> **Status: under active construction.** This system is being built incrementally across
> sequential PRs, each delivering a small, independently demoable unit of work. Expect
> directories described below to fill in over time — see
> [`.ai-agents/PLAN.md`](.ai-agents/PLAN.md) for the full roadmap and current progress.

## What this is

A rewrite of a monolithic prototype (kept for reference in
[`legacy/monolith-reference`](legacy/monolith-reference)) that synchronously accepted a video
upload, ran `ffmpeg` to extract frames, zipped them, and returned the archive in one HTTP
response — with no auth, no persistence, and no way to scale processing independently of the API.

The target system splits that into authenticated, horizontally-scalable microservices connected
by a message queue, with persistent job status, failure notifications, and monitoring. See
[`docs/architecture.md`](docs/architecture.md) for the full design, a component diagram, and the
reasoning behind each infrastructure choice (also recorded as ADRs in
[`docs/adr/`](docs/adr/)).

## Repository layout

```
/services                    Independently deployable Go services (one module each)
  /auth-service               Registration, login, auth
  /video-service               Public video API: upload, status, download
  /worker-service               ffmpeg processing, scaled horizontally
  /notification-service          Failure notifications
/gateway                      Edge/API gateway (reserved)
/deploy                       Deployment configuration
  docker-compose.yml            Local/demo orchestration
  /db                            Database init scripts
  /monitoring                    Prometheus + Grafana configuration
/pkg                          Shared Go packages (reserved)
/docs                         Architecture docs and ADRs
/legacy/monolith-reference    Original prototype, kept for reference only
.github/workflows             CI/CD pipelines
go.work                       Go workspace wiring the per-service modules together
```

## Getting started

Each service under `/services` is currently a stub (`// TODO: implemented in a later plan item`)
so the workspace builds end-to-end while services are filled in one at a time.

```bash
# Build every service via the Go workspace
go build ./...

# Bring up infrastructure (Postgres, Redis, RabbitMQ, Prometheus, Grafana)
# plus the (currently stubbed) application services
cd deploy
docker compose up
```

Once running: RabbitMQ management UI at `http://localhost:15672` (fiapx/fiapx), Prometheus at
`http://localhost:9090`, Grafana at `http://localhost:3000` (admin/admin).

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — component diagram, data flow, and technology
  rationale.
- [`docs/adr/`](docs/adr/) — architecture decision records.
- [`.ai-agents/PLAN.md`](.ai-agents/PLAN.md) — the delivery roadmap this project follows.
