# 0002 — RabbitMQ over Kafka for inter-service messaging

## Context

`video-service` must hand off video-processing jobs to `worker-service` asynchronously so the
upload HTTP request returns immediately instead of blocking on `ffmpeg`, and `worker-service`
must be able to notify `notification-service` of failures. We need a message broker that:
decouples producers from consumers, survives broker restarts without losing queued work, supports
per-message acknowledgement so a crashed worker doesn't silently drop a job, and is simple enough
to operate for a small team on a hackathon timeline. The realistic choices considered were
RabbitMQ and Kafka.

## Decision

Use RabbitMQ (with durable queues and manual/publisher acknowledgements) as the message broker
between `video-service`, `worker-service`, and `notification-service`.

## Consequences

- RabbitMQ's work-queue model (competing consumers, manual ack, per-message redelivery on
  consumer failure) maps directly onto "distribute jobs across N worker replicas, and don't lose
  a job if a worker dies mid-processing" — the core reliability requirement here.
- Operationally simpler than Kafka for this scale: a single container, no partition/broker
  cluster sizing decisions, and a built-in management UI for inspecting queues during
  development and the demo.
- We give up Kafka's strengths that this project doesn't need: high-throughput log-structured
  storage, long-term event replay, and consumer-group rebalancing across many partitions. Job
  volume here is bounded by individual video uploads, not a high-throughput event stream.
- If a future requirement needs durable event replay or very high throughput fan-out, that would
  be reason to revisit this decision.
