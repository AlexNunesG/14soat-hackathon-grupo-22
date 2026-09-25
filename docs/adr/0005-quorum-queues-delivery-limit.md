# 0005. Quorum queues with x-delivery-limit for video.process and video.notify

- Status: Accepted
- Date: 2026-09-25

## Context

PLAN.md §5.3 and `docs/messaging.md` ("Consumption, retries and
dead-lettering") describe the worker's own retry/backoff for `video.process`:
on a retryable failure it republishes to `video.process.retry.n` with an
`attempt` header, and gives up (dead-letters to `video.process.dlq`, after
recording the video `FAILED`) once `attempt` reaches `WORKER_MAX_ATTEMPTS`.
That only works if `Consumer.Handle` gets to run at all. If the worker
process itself crashes, is OOM-killed, or panics **before** it acks — e.g.
every time it runs ffmpeg on one poisonous input — the message is never
acked, and the broker's own crash recovery puts it back at the front of the
queue for redelivery. On a **classic** queue that redelivery has no cap:
RabbitMQ does not track how many times a classic queue has redelivered a
given message, so the job is retried forever, on every worker replica in
turn, without ever incrementing the `attempt` header (nothing ran) and
without ever reaching `GiveUp`. The video stays `PROCESSING` forever and the
owner is never notified (RF5).

The same crash-loop risk applies to every retry/delay queue in both work
queue topologies (`video.process`, `video.process.retry.1..3`,
`video.notify`, `video.notify.retry.1..4`): any of them can in principle
hold an unacked message across a crash.

**Quorum queues** track delivery attempts natively per queue
(`x-delivery-count`, visible on the message once redelivered) and support
`x-delivery-limit`: once a message has been returned to the queue more than
the limit, RabbitMQ dead-letters it itself, with reason
`delivery_limit` — independent of whether any consumer ever ran its
handler. That closes exactly the gap classic queues leave open.

## Decision

1. **`video.process`, `video.process.retry.1..3`, `video.notify` and
   `video.notify.retry.1..4` become quorum queues** (`x-queue-type:
   quorum`), each with `x-delivery-limit` set to `WorkQueue.DeliveryLimit()`
   = `MaxAttempts() + 1` (`internal/adapters/rabbitmq/topology.go`): 5 for
   `video.process` (4 app-level attempts + 1), 6 for `video.notify` (5 + 1).
   The `+1` keeps the application's own attempt-count logic as the primary
   mechanism and RabbitMQ's delivery-limit strictly a **backstop**: on a
   healthy consumer, the application always gives up (records `FAILED`,
   nacks without requeue) at or before its own `MaxAttempts`, so the
   broker's limit is never the one to fire. It only fires for exactly the
   scenario in Context: a consumer that never got to run the handler.
   Per-message and per-queue TTL and a dead-letter exchange/routing-key
   (used by the retry queues and the work queues) work the same on quorum
   queues as on classic ones, so no other queue argument changes.

2. **The dead-letter queues (`video.process.dlq`, `video.notify.dlq`) stay
   classic**, with no `x-delivery-limit`. They are terminal
   inspection/replay queues; their only consumer (the reconciler, §4 below,
   and the notifier's DLQ has none yet) never runs anything crash-prone
   like ffmpeg — a reconcile failure is a transient infrastructure error
   (e.g. the database down), handled by nacking with requeue, not by a
   delivery count. Making them quorum too would need their own dead-letter
   exchange to be meaningful (a quorum queue with a delivery-limit but no
   DLX simply **drops** a message past the limit), which is unwarranted
   complexity for a queue only ever touched by the reconciler and manual
   replay via the management UI.

3. **No `x-quorum-initial-group-size`.** This project's RabbitMQ is
   single-node everywhere it runs today (compose, CI, the k8s dev overlay's
   `StatefulSet` at `deploy/k8s/base/infra/rabbitmq/statefulset.yaml`), so
   the default replication factor of 1 is already correct; setting a
   replica count higher than the cluster size would make declaration fail.

4. **`x-max-priority` is not used anywhere** in the topology (verified: no
   priority argument in `internal/adapters/rabbitmq/topology.go` or
   `deploy/rabbitmq/definitions.json`), so quorum queues' lack of priority
   support is not a regression.

5. **RabbitMQ version**: `deploy/docker-compose.yml` and
   `deploy/k8s/base/infra/rabbitmq/statefulset.yaml` both already run
   `rabbitmq:4-management-alpine`. Quorum queues (3.8+) and
   `x-delivery-limit` (3.10+) are supported; no version bump is needed.

6. **Migration story: wipe the dev volume, don't migrate in place.** Queue
   type is immutable after creation — redeclaring an existing queue with a
   different `x-queue-type` fails with `PRECONDITION_FAILED`. Every service
   declares this topology on every connection
   (`internal/adapters/rabbitmq/topology.go`, `declare`), so upgrading a
   RabbitMQ that already has the old classic queues on disk will fail to
   start any service until the queues are gone. For this project's scope
   (a hackathon's dev/CI queues, not a production migration), the answer is:
   **delete the RabbitMQ volume**. `deploy/docker-compose.yml`'s
   `rabbitmq-data` is disposable dev state; `make down` already runs
   `docker compose down -v`, and CI always starts from a fresh volume, so
   neither needs a code change. Anyone with an existing local volume from
   before this change runs `docker compose -f deploy/docker-compose.yml
   down -v` (or `make down`) once before `make up`. A real production
   migration (create new queues under new names, cut consumers/publishers
   over, drain and delete the old ones) is out of scope here and not worth
   building for queues nobody has real traffic on yet.

7. **Reconciling the DLQ (the gap this feature itself opens).** When
   RabbitMQ's own `x-delivery-limit` dead-letters a `video.process` message,
   the worker's `Processor.Handle`/`GiveUp` never ran for it — that is the
   whole point of the feature — so nothing marked the video `FAILED` or
   queued its `video.failed` event, and the video is stuck `PROCESSING`
   forever even though its job is safely sitting in `video.process.dlq`.
   Considered:
   - **(a) A consumer on the DLQ** that reruns the worker's own
     mark-failed logic against the dead-lettered message.
   - **(b) A periodic reconciliation query** for videos stuck
     `PENDING`/`PROCESSING` past some timeout.
   - Chosen: **(a)**. It reuses `Processor.GiveUp` exactly (decode the job,
     load the video, mark it `FAILED` with its `video.failed` event, all
     already idempotent and safe on a redelivered/duplicate call) — as
     `Processor.Reconcile`, the only new application code is one line
     (`internal/app/processor.go`). It reacts immediately (no timeout to
     tune, so no risk of a video sitting FAILED-but-not-yet-reconciled
     for an arbitrary window), needs no new schema or query, and keeps
     "what happens to a job" a broker/queue concern rather than adding a
     second, timeout-based path that can mark a video `FAILED` while a
     slow-but-still-running job later completes it (a wrong guess on
     the timeout in (b) either fires too early, on a video no crash-loop
     ever affected, or too late). (b) would still be worth adding later as
     a defence-in-depth safety net (e.g. "PROCESSING for over 1 hour" is
     always a bug worth paging on, however it happened), but is out of
     scope for this ADR.

     Implementation: `rabbitmq.DeadLetterConsumer`
     (`internal/adapters/rabbitmq/reconciler.go`), run by the worker
     (`cmd/worker/main.go`) alongside its ordinary `video.process`
     consumer, consuming `video.process.dlq` with manual acks and a
     prefetch of 1. Every message there — whichever gave up on it, the
     application's own `GiveUp` or RabbitMQ's `x-delivery-limit` — is
     handed to `Processor.Reconcile`, which calls the same `GiveUp` used
     by the ordinary give-up path, with a distinct cause
     (`app.ErrDeliveryLimitExceeded`) so the video's `error_message`
     names what actually happened. A reconcile failure (e.g. the database
     down) is nacked with requeue after a short delay and retried in
     place, rather than looping tightly or being dropped; a malformed body
     or an already-final/unknown video is a no-op, exactly like `GiveUp`.

## Consequences

- A `video.process` job whose consumer crashes on every attempt now
  terminates after `x-delivery-limit` (5) redeliveries instead of looping
  forever, and the owner is notified (RF5) within one DLQ-reconcile pass
  instead of the video staying stuck `PROCESSING` indefinitely.
- One more moving part in the worker: the DLQ reconciler consumer, run by
  every worker replica (competing consumers on the same DLQ, so it survives
  a replica going down like every other consumer here).
- Operators reading `video.process.dlq` should expect two kinds of message
  now: the application's own give-up (as before) and RabbitMQ's
  `x-delivery-limit` give-up (`x-first-death-reason: delivery_limit` in the
  message's headers) — both are now reconciled (the video marked `FAILED`)
  the same way.
- Anyone with a pre-existing local `rabbitmq-data` volume must wipe it
  (`make down` / `docker compose down -v`) once before `make up`, or queue
  declaration fails with `PRECONDITION_FAILED` (§6). CI is unaffected (it
  always starts from a fresh volume).
- Quorum queues have a small per-message overhead (Raft consensus) versus
  classic queues; irrelevant at this project's throughput.

## Later changes

- A periodic "stuck in PROCESSING/PENDING too long" reconciliation query
  (option (b) above) would add defence-in-depth against any path that
  still leaves a video non-final forever (not just this one), independent
  of RabbitMQ. Not implemented here.
