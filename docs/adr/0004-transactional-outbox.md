# 0004. Transactional outbox for processing jobs

- Status: Accepted
- Date: 2026-09-24

## Context

RF2 ("never lose a request during load peaks") and the contract
(`docs/openapi.yaml`: "A `202` means the job is durably queued and will not
be lost") require that every accepted upload is eventually processed. An
upload touches three systems: the object storage (the file), PostgreSQL
(the `videos` row, `PENDING`) and RabbitMQ (the job message). There is no
transaction spanning PostgreSQL and RabbitMQ, so a naive "insert, then
publish" can lose the job when the broker is down or the api crashes between
the two steps, and "publish, then insert" can queue a job for a video that
does not exist.

PLAN.md §4 and Phase 2.3 left two options open: a transactional outbox, or
publish-then-commit with a reconciliation job.

## Decision

Use a **transactional outbox** in the api:

1. The upload stores each file in the object storage, then inserts the
   videos (`PENDING`) **and** one outbox row per video (routing key
   `video.uploaded`, message id = video id, JSON payload `{"video_id"}`) in
   **one PostgreSQL transaction** (`db/migrations/00002_create_outbox.sql`).
   The api answers `202` after the commit. If storing or the transaction
   fails, the stored objects are removed (best effort) and the request fails:
   nothing visible is left behind.
2. A **relay** goroutine in every api replica publishes the outbox. Each
   pass, in one transaction, claims up to `OUTBOX_BATCH_SIZE` due rows
   (`WHERE available_at <= now() ORDER BY id ... FOR UPDATE SKIP LOCKED`),
   publishes them as persistent, mandatory messages and waits for the
   **publisher confirms**, then deletes the confirmed rows and postpones the
   failed ones (`attempts + 1`, `last_error`, exponential backoff 1 s, 2 s,
   4 s … capped at 15 s). `SKIP LOCKED` lets several replicas relay at once
   without publishing the same row twice concurrently.
3. The relay runs after every upload (in-process notification), every
   `OUTBOX_POLL_INTERVAL` (default 1 s) for leftovers, and once more on
   shutdown after the HTTP server has drained.

Delivery is **at least once**: a crash after the broker confirmed a message
but before the row was deleted publishes it again. The worker is idempotent
(conditional status updates; a job for a `DONE`/`FAILED` video is ignored),
so duplicates are harmless.

Why not publish-then-commit with reconciliation: it needs a separate job
that scans `PENDING` videos older than some threshold and republishes them,
and a threshold that can neither be too short (duplicates of jobs still in
the queue) nor too long (slow recovery). The outbox has no such timing
guess, keeps uploads working while RabbitMQ is down (the jobs wait in
PostgreSQL), and its state is inspectable with SQL
(`SELECT * FROM outbox`).

## Consequences

- An upload succeeds while RabbitMQ is down; its video stays `PENDING` until
  the broker is back and the relay publishes it. `/readyz` still reports the
  broker as failing.
- The latency between upload and publish is normally milliseconds (the
  upload wakes the relay), at most `OUTBOX_POLL_INTERVAL` for rows left by
  another replica, and up to the backoff cap after broker failures.
- The outbox is one more table to watch: a growing `outbox` means the broker
  is unreachable or rejecting messages (`last_error` says why). Rows are
  deleted once published, so the table stays small.
- The payload column is `jsonb`: the published body is JSON-equivalent to
  the original, not byte-identical.
- Publishing is serialized per replica (one relay, batches of up to
  `OUTBOX_BATCH_SIZE`); more throughput comes from more api replicas or a
  bigger batch.
- A crash between storing a file and committing the transaction can leave an
  orphan object in the storage (never a visible video). A storage lifecycle
  rule or a sweeper can remove `uploads/*` objects without a `videos` row if
  that ever matters.

## Later changes

- Phase 2.5 (RF5): the worker uses the same outbox for the `video.failed`
  and `video.processed` events. It inserts them in the transaction that
  changes the video's status, and runs its own relay. Every relay (api and
  worker) declares the whole topology and publishes any row. See
  [`docs/notifications.md`](../notifications.md).
