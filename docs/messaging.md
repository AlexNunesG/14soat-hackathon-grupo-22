# Messaging (RabbitMQ)

Processing is asynchronous (RF1, RF2): the api queues one job per uploaded
video and the workers consume the jobs. When a video reaches a final status
the worker records an event, and the notifier e-mails the owner of every
failed video (RF5, [`notifications.md`](notifications.md)). This file and
[`deploy/rabbitmq/definitions.json`](../deploy/rabbitmq/definitions.json)
are the broker part of deliverable **D2**.

## Topology

Declared as code (`internal/adapters/rabbitmq/topology.go`,
`rabbitmq.VideoProcess`, `rabbitmq.VideoNotify`) every time a service
connects: the outbox relays (api and worker) declare the whole topology,
each consumer declares its own work queue. An empty broker needs no setup. Everything is durable; queues are classic
queues (`x-queue-type: classic`).

| Resource | Kind | Arguments / binding | Role |
|---|---|---|---|
| `videos` | topic exchange | | Every video event is published here. |
| `video.process` | queue | bound to `videos` with `video.uploaded`; DLX `videos.dlx`, DL key `video.process` | Processing jobs, consumed by the workers. |
| `video.process.retry.1` / `.2` / `.3` | queues | TTL 2 s / 10 s / 30 s; DLX `""` (default exchange), DL key `video.process` | Delay before retry 1 / 2 / 3. No consumer: expired messages go back to `video.process`. |
| `videos.dlx` | direct exchange | | Dead-letter exchange, routed by the name of the queue the message died in. |
| `video.process.dlq` | queue | bound to `videos.dlx` with `video.process` | Jobs given up on, for inspection or manual replay. |
| `video.notify` | queue | bound to `videos` with `video.failed`; DLX `videos.dlx`, DL key `video.notify` | Failure events, consumed by the notifier. |
| `video.notify.retry.1` … `.4` | queues | TTL 10 s / 1 min / 5 min / 15 min; DLX `""`, DL key `video.notify` | Delay before e-mail retry 1 … 4 (about 21 minutes in total, enough to ride out a mail server restart). |
| `video.notify.dlq` | queue | bound to `videos.dlx` with `video.notify` | Failure events whose e-mail could not be sent. |

```
api (outbox relay) --video.uploaded--> [videos] --> video.process --> worker
                                                        ^   |  \
                        TTL 2s/10s/30s expired           |   |   nack (last attempt, malformed)
             video.process.retry.N  ------------------------+   |        \
                    ^                                           |         [videos.dlx] --> video.process.dlq
                    +---- worker republishes (attempt N+1) -----+

worker (outbox relay) --video.failed--> [videos] --> video.notify --> notifier --SMTP--> mail server
                      --video.processed--> [videos] (no queue bound yet: dropped)
          video.notify.retry.1..4 (10s/1m/5m/15m) and video.notify.dlq work like the video.process ones
```

`definitions.json` holds the same resources in RabbitMQ's definitions
format; a unit test fails when it drifts from the code (regenerate with
`go test ./internal/adapters/rabbitmq/ -run TestDefinitionsFile -update`).
Import it by hand with `rabbitmqctl import_definitions definitions.json` or
the management UI (Overview → Import definitions). It is not loaded at
broker boot, because boot-time definitions replace the default user that
compose creates from `RABBITMQ_USER`/`RABBITMQ_PASSWORD`.

## Messages

| Routing key | Recorded by | Body | Message id | Queue |
|---|---|---|---|---|
| `video.uploaded` | api, with the upload | `{"video_id": "<uuid>"}` | the video id | `video.process` |
| `video.failed` | worker, with the change to `FAILED` | video event (below) | the event id | `video.notify` |
| `video.processed` | worker, with the change to `DONE` | video event (below) | the event id | none yet |

Every message is written to the outbox in the same transaction as the
change it announces ([ADR 0004](adr/0004-transactional-outbox.md)) and
published by an outbox relay. The api and every worker run a relay, and all
relays use the same publisher (`rabbitmq.NewOutboxPublisher`), so any
replica can publish any row.

Messages are persistent (`delivery_mode 2`), JSON, and published with
**publisher confirms**: a message counts as sent only once the broker
confirmed it. `video.uploaded` and `video.failed` are **mandatory**: when no
queue is bound, the broker returns the message, the relay counts it as
failed and tries again later. It is never dropped silently. `video.processed`
has no consumer yet (a "frames ready" e-mail or metrics could use it later),
so it is listed in `rabbitmq.UnroutedTopics` and published **without**
mandatory: the broker confirms it and drops it while no queue is bound, and
the outbox does not fill up with events nobody reads. Binding a queue to
`video.processed` later needs no change in the worker. The header `attempt`
(absent = 1) counts deliveries across retries.
The standard `correlation-id` property carries the request id of the upload
that caused the message (its `X-Request-ID`): the outbox stores it with the
row, consumers restore it into the context of their handler, and the
worker's events inherit it from the job, so the logs of one upload can be
followed across services ([`observability.md`](observability.md)). Retries
keep it.

### Video events

`video.failed` and `video.processed` carry the same JSON object
(`app.VideoEvent`). Consumers need no database read to act on it:

```json
{
  "event_id": "7338173f-fa64-40b3-bd2a-1a5f630216dc",
  "type": "video.failed",
  "video_id": "ee00da0d-d3d6-4841-a130-f73336a59e7c",
  "owner_id": "5c1d2c8e-9d0e-4a53-9a47-3f0b0b8f6f11",
  "owner_email": "ana@example.com",
  "owner_name": "Ana",
  "original_name": "férias corrompidas.mp4",
  "uploaded_at": "2026-09-24T19:33:39.512345Z",
  "error_message": "ffmpeg: Error opening input files: Invalid data found when processing input",
  "occurred_at": "2026-09-24T19:33:40.101234Z"
}
```

`error_message` is set only on `video.failed`, and `frame_count` only on
`video.processed`. The owner's e-mail and name are read when the event
occurs, just before the transaction that changes the status. The event id
is a fresh UUID. Only the run whose conditional `UPDATE` wins writes the
event, so each final status has exactly one event.

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

## Notifications (notifier)

The notifier consumes `video.notify` with manual acks, a prefetch of
`NOTIFIER_CONCURRENCY` (default 4) and the same consumer as the worker.
It sends one e-mail per `video.failed` event:

| Outcome | Settlement |
|---|---|
| E-mail accepted by the SMTP server, or already sent for this event id | ack |
| Transient failure (connection refused or timed out, 4xx reply, database down) on attempt n < 5 (`NOTIFIER_MAX_ATTEMPTS`) | republish to `video.notify.retry.n`, then ack |
| Transient failure on the last attempt | logged, nack without requeue → `video.notify.dlq` |
| Permanent failure (5xx reply to MAIL FROM / RCPT TO / DATA, e.g. an unknown recipient; an invalid address) | nack without requeue → `video.notify.dlq` at once (handler error wraps `app.ErrPermanent`) |
| Malformed event | nack without requeue → DLQ |

Redeliveries are deduplicated by event id in the table `notifications_sent`
([`notifications.md`](notifications.md#exactly-one-e-mail)).

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
then canceled (ffmpeg is killed) and requeued. Its outbox relay then makes a
last pass, so the events of the last jobs are published (anything left is
published by another replica, or by the api, on its next pass). The
notifier shuts down the same way; an e-mail interrupted mid-send is
requeued and, not being recorded as sent, sent again. When the connection
drops, a consumer reconnects with backoff (0.5 s … 5 s) and declares the
topology again; publishers reconnect on their next publish.
