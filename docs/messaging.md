# Messaging (RabbitMQ)

Processing is asynchronous (RF1, RF2): the api queues one job per uploaded
video and the workers consume the jobs. This file and
[`deploy/rabbitmq/definitions.json`](../deploy/rabbitmq/definitions.json)
are the broker part of deliverable **D2**.

## Topology

Declared as code (`internal/adapters/rabbitmq/topology.go`,
`rabbitmq.VideoProcess`) by the api and the worker every time they connect,
so an empty broker needs no setup. Everything is durable; queues are classic
queues (`x-queue-type: classic`).

| Resource | Kind | Arguments / binding | Role |
|---|---|---|---|
| `videos` | topic exchange | | Every video event is published here. |
| `video.process` | queue | bound to `videos` with `video.uploaded`; DLX `videos.dlx`, DL key `video.process` | Processing jobs, consumed by the workers. |
| `video.process.retry.1` / `.2` / `.3` | queues | TTL 2 s / 10 s / 30 s; DLX `""` (default exchange), DL key `video.process` | Delay before retry 1 / 2 / 3. No consumer: expired messages go back to `video.process`. |
| `videos.dlx` | direct exchange | | Dead-letter exchange, routed by the name of the queue the message died in. |
| `video.process.dlq` | queue | bound to `videos.dlx` with `video.process` | Jobs given up on, for inspection or manual replay. |

```
api (outbox relay) --video.uploaded--> [videos] --> video.process --> worker
                                                        ^   |  \
                        TTL 2s/10s/30s expired           |   |   nack (last attempt, malformed)
             video.process.retry.N  ------------------------+   |        \
                    ^                                           |         [videos.dlx] --> video.process.dlq
                    +---- worker republishes (attempt N+1) -----+
```

`definitions.json` holds the same resources in RabbitMQ's definitions
format; a unit test fails when it drifts from the code (regenerate with
`go test ./internal/adapters/rabbitmq/ -run TestDefinitionsFile -update`).
Import it by hand with `rabbitmqctl import_definitions definitions.json` or
the management UI (Overview → Import definitions). It is not loaded at
broker boot, because boot-time definitions replace the default user that
compose creates from `RABBITMQ_USER`/`RABBITMQ_PASSWORD`.

## Messages

| Routing key | Publisher | Body | Message id |
|---|---|---|---|
| `video.uploaded` | api (outbox relay, [ADR 0004](adr/0004-transactional-outbox.md)) | `{"video_id": "<uuid>"}` | the video id |

Messages are persistent (`delivery_mode 2`), JSON, and published as
mandatory with **publisher confirms**: a message counts as sent only once the
broker confirmed it; an unroutable one is reported, never dropped silently.
The header `attempt` (absent = 1) counts deliveries across retries.

## Consumption, retries and dead-lettering

The worker consumes `video.process` with manual acks and a prefetch equal to
its concurrency (`WORKER_CONCURRENCY`, default 2), so a worker never holds
more jobs than it runs. For each job:

| Outcome | Settlement | Video |
|---|---|---|
| Frames extracted and zip stored | ack | `DONE` |
| The file cannot be processed (undecodable, no video stream, no frames, ffmpeg timeout, upload missing) | ack (no retry: it would fail again) | `FAILED` with the reason |
| Transient failure (storage, database, …) on attempt n < 4 | republish to `video.process.retry.n` with `attempt = n+1` (confirmed), then ack | stays `PROCESSING` |
| Transient failure on attempt 4 (`WORKER_MAX_ATTEMPTS`, at most 4) | video marked `FAILED`, then nack without requeue → `video.process.dlq` | `FAILED` ("processing failed after several attempts: …") |
| Malformed body | nack without requeue → DLQ | — |
| Worker shutting down (job canceled after `SHUTDOWN_TIMEOUT`) | nack with requeue | stays `PROCESSING` until redelivered |
| Worker crash / connection lost | none: the broker redelivers the unacked job | stays `PROCESSING` until redelivered |

## Idempotency

Delivery is at least once (outbox republish, redelivery after a crash), so
the worker tolerates duplicates:

- a job for an unknown, `DONE` or `FAILED` video is acked and ignored;
- `PENDING` or `PROCESSING` → `PROCESSING` is allowed, so a redelivered job
  after a crash restarts the work from scratch;
- every status change is a conditional `UPDATE ... WHERE status IN (...)`;
- each run writes its zip to a fresh key (`frames/<video id>/<run id>.zip`);
  only the first run to record `DONE` wins, the others delete their zip, so a
  `DONE` video's zip is never overwritten.

## Graceful shutdown and reconnection

On SIGTERM the worker cancels its consumer, requeues what it received but
did not start, and lets in-flight jobs finish for up to `SHUTDOWN_TIMEOUT`
(default 15 s; compose `stop_grace_period` is 20 s); jobs still running are
then canceled (ffmpeg is killed) and requeued. When the connection drops,
the consumer reconnects with backoff (0.5 s … 5 s) and declares the topology
again; the api's publisher reconnects on its next publish.
