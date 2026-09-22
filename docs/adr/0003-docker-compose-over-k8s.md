# 0003 — Docker Compose over Kubernetes for orchestration

## Context

The system is made up of four Go services plus PostgreSQL, Redis, RabbitMQ, Prometheus, and
Grafana. It needs to run reliably on a single developer laptop and on whatever host is used for
the hackathon demo, be startable/stoppable with one command, and support demonstrating horizontal
scaling of `worker-service` (multiple replicas processing jobs concurrently). We considered
Kubernetes (e.g. via a local cluster like kind/minikube) against Docker Compose.

## Decision

Use Docker Compose (`deploy/docker-compose.yml`) as the orchestration mechanism for local
development and the demo environment, including scaling `worker-service` via
`docker compose up --scale worker-service=N`.

## Consequences

- One file, one command (`docker compose up`) brings up the full stack — services and infra —
  with no cluster bootstrap step, which matters for a hackathon timeline and for judges/reviewers
  who need to run the system quickly.
- `docker compose --scale` is sufficient to demonstrate concurrent multi-replica processing,
  which is the specific scaling requirement this project needs to show.
- We give up Kubernetes features this project doesn't need at this stage: multi-node scheduling,
  rolling deployments, autoscaling policies, and service mesh integration.
- Tradeoff: Compose's scaling and self-healing are far more limited than Kubernetes's — acceptable
  here because the target environment is a single host, not a production multi-node cluster.
- If the project later needs multi-host deployment, autoscaling, or zero-downtime rolling
  deploys, that would be reason to revisit this decision and migrate to Kubernetes.
