# Load test — RF2 "no lost requests during a spike"

Phase 4 item 4 of [`.ai-agents/PLAN.md`](../../.ai-agents/PLAN.md): a k6 load
test that ramps `POST /api/v1/videos` traffic from a baseline up to a spike
and back down, and proves that (a) the HTTP layer never drops or fails a
request during that spike, and (b) every request the API accepted is later
actually finished by the backend — reaches `DONE` or `FAILED`, never stuck.
That second half is the real "not lost" proof: a video the API accepted
(202) but which never finishes processing is, in this system's terms, a
lost request even though the HTTP call itself succeeded (see
[`docs/messaging.md`](../messaging.md) for the outbox/queue path RF2 relies
on).

## What was tested and how

Scripts: [`deploy/loadtest/spike.js`](../../deploy/loadtest/spike.js) and
[`deploy/loadtest/confirm.js`](../../deploy/loadtest/confirm.js), run via
`make loadtest` (see the `loadtest` target in the root
[`Makefile`](../../Makefile)) against the local compose stack
(`deploy/docker-compose.yml`), as two sequential `k6 run` invocations in
`grafana/k6:latest` containers on the compose network
(`video-processor_default`):

1. **`spike.js`** — `setup()` registers and logs in 8 test users (spreads
   load like real traffic and avoids a single user's pagination/ownership
   path becoming the bottleneck), then a `ramping-arrival-rate` scenario
   fires `POST /api/v1/videos` with a tiny generated fixture video
   (`deploy/loadtest/tiny.mp4`: 64x48, ~1s, mpeg4 — fast for ffmpeg to
   decode and zip, so the backend can actually drain the queue during the
   observation window), round-robining across the 8 users' tokens. Every
   response is `check()`-ed for HTTP 202 and recorded in custom k6 metrics
   (`Counter`/`Rate`), **not** `thresholds` — thresholds can abort a run
   mid-flight, and the goal is a full run plus an end-of-run verdict.
   Every accepted id (and which user uploaded it) is logged to stdout,
   which the Makefile target captures to `spike.log`.
2. **`confirm.js`** — a separate k6 process, started only after `spike.js`
   has fully exited. At its own init time it reads `spike.log` back (k6's
   `open()` only works in the init context of a fresh process, which is
   why this is a second script rather than a second scenario of the same
   run — see the comments at the top of both files for the full reasoning)
   to recover every accepted id and its owner's token, then runs a single
   VU/single iteration that polls `GET /api/v1/videos/{id}` for every id,
   in batched rounds with a sleep between rounds, until each reaches a
   final status or a timeout (180s default, `LOADTEST_CONFIRM_TIMEOUT_S`).

Both scripts write a JSON summary (`spike-summary.json`,
`confirm-summary.json`, via `handleSummary()`) and a Markdown report
(`spike-report.md`, `confirm-report.md`) into this directory, plus the full
captured k6 stdout (`spike.log`, `confirm.log`).

### Load profile (spike.js, `ramping-arrival-rate`)

| stage | window | target rate | shape |
|---|---|---|---|
| baseline | 0s – 10s | 5 req/s | flat |
| spike | 10s – 25s | 5 → 30 req/s | ramp up |
| back to baseline | 25s – 35s | 30 → 5 req/s | ramp down |
| ramp down | 35s – 40s | 5 → 0 req/s | ramp down |

`preAllocatedVUs: 50`, `maxVUs: 120`. Total spike-phase wall time: 40s.
`confirm.js`'s polling budget (180s, adjustable) runs after that.

### Pass/fail criteria

- **Phase 1 (spike.js):** zero non-202 responses and zero transport errors
  across the whole run (`uploads_rejected` counter and k6's own
  `http_req_failed` rate both 0).
- **Phase 2 (confirm.js):** every id accepted in phase 1 reaches `DONE` or
  `FAILED` within the confirm timeout (`confirmed_lost` counter is 0).
- Both are computed in each script's `handleSummary()` from the completed
  run's metrics, not via aborting `thresholds`.

## Results — two runs

Run with `make loadtest` (real, against the local compose stack: postgres,
redis, rabbitmq, seaweedfs storage, api, 2 worker replicas, notifier,
prometheus, grafana). Both runs used the same profile and fixture video.

| | Run 1 | Run 2 |
|---|---|---|
| Uploads accepted (202) | 499 | 499 |
| Uploads rejected (non-202/error) | 0 | 0 |
| Total HTTP requests (incl. register/login) | 515 | 515 |
| `http_req_failed` rate (k6 built-in) | 0.000% | 0.000% |
| Upload latency avg | 7.12 ms | 7.08 ms |
| Upload latency p50 | 6.09 ms | 6.52 ms |
| Upload latency p90 | 8.06 ms | 7.59 ms |
| Upload latency p95 | 10.76 ms | 8.24 ms |
| Upload latency p99 | 20.72 ms | 10.65 ms |
| Upload latency max | 136.37 ms | 230.96 ms |
| Accepted ids confirmed DONE/FAILED | 499 / 499 | 499 / 499 |
| Of those, status FAILED (not DONE) | 0 | 0 |
| Accepted ids never reaching a final status (lost) | 0 | 0 |
| Confirm phase wall time | < 2s (all 499 already final on first poll) | < 2s (all 499 already final on first poll) |

Both runs are in this directory as the current `spike.log` / `confirm.log`
/ `*-summary.json` / `*-report.md` (run 2, the more recent of the two —
run 1 was byte-identical in outcome, just captured separately during
validation and not kept alongside these to avoid duplicate large log
files).

**RF2 verdict, both runs: PASS — no request was lost: 499/499 accepted
uploads reached DONE/FAILED, 0 non-202 responses, 0 stuck/lost.**

By the numbers achieved: the run generated 515 total HTTP requests over
~43s wall time (~12 req/s average, peaking well above that during the
spike stage per k6's own iteration-rate readout, which showed ~16 iters/s
early in the spike and tapering as the ramp-down and confirm phase queued
work). This is a modest rate by design (see `deploy/loadtest/spike.js`'s
comments) — ballasted to what a 4-slot worker pool (2 `worker` replicas x
`WORKER_CONCURRENCY=2`, see `deploy/docker-compose.yml`) processing
~1-second fixture videos can fully drain within the observation window,
while still demonstrating a clear ramp from baseline to several times that
rate and back.

### Prometheus corroboration (nice-to-have)

Queried at `http://localhost:9091` right after a run (api/worker/notifier
containers had just been (re)started by `make up`'s `--build --wait`, so
these are clean, from-zero cumulative counters for that run):

```
videoproc_http_requests_total{method="POST",route="/api/v1/videos",status="202"} 499
videoproc_http_requests_total{method="GET",route="/api/v1/videos/:id",status="200"} 499
videoproc_http_requests_total{method="POST",route="/api/v1/auth/register",status="201"} 8
videoproc_http_requests_total{method="POST",route="/api/v1/auth/login",status="200"} 8

videoproc_jobs_total{outcome="done"} 249   # worker replica 1
videoproc_jobs_total{outcome="done"} 250   # worker replica 2
videoproc_jobs_total{outcome="failed"}        0  (both replicas)
videoproc_jobs_total{outcome="dead_lettered"} 0  (both replicas)
videoproc_jobs_total{outcome="requeued"}      0  (both replicas)
videoproc_jobs_total{outcome="retried"}       0  (both replicas)

videoproc_outbox_pending 0   # api and both workers, i.e. nothing left in the outbox
```

`249 + 250 = 499` jobs done across the two worker replicas exactly matches
the 499 accepted uploads and the 499 confirmed-DONE videos above, with
zero failures, zero dead-lettered messages, zero requeues and an empty
outbox — consistent, independent confirmation from the metrics pipeline
(see [`docs/observability.md`](../observability.md)) that nothing was lost
or stuck anywhere along the outbox → RabbitMQ → worker path.

## Running it yourself

```sh
make up          # start the local stack (idempotent if already up)
make loadtest     # runs spike.js then confirm.js; results land here
make down         # tear the stack down when finished (optional)
```

Override knobs via env vars passed through the Makefile target, e.g.:

```sh
LOADTEST_NETWORK=myproject_default make loadtest   # different compose project name
```

or by editing `deploy/loadtest/spike.js`'s `USER_COUNT`/`stages` and
`deploy/loadtest/confirm.js`'s `CONFIRM_TIMEOUT_S`/`CONFIRM_POLL_INTERVAL_S`
env-var defaults directly.

Both `docker run --rm` k6 containers clean themselves up; no stray
containers are left behind by the test itself.

## Notes / things that looked odd but resolved cleanly

- k6's `open()` only works in a script's init context, and there is no
  cross-VU or cross-scenario shared *write* state in open-source k6 (no
  file-write API from within a running script). A single-run,
  two-scenario design (spike scenario chained into a confirm scenario via
  `startTime`) therefore cannot pass the spike scenario's accepted ids to
  the confirm scenario. This is why the test is two separate `k6 run`
  processes chained by the Makefile, with the first one's captured stdout
  (`spike.log`) as the hand-off channel, read back by the second script's
  own init context. Documented in both scripts' header comments.
- The `k6-summary` jslib (`https://jslib.k6.io/...`) is not reachable from
  the k6 container's network (compose network only, no general internet
  route in this sandbox), so `handleSummary()` builds its own small
  hand-written text/Markdown summary instead of importing that jslib.
- The k6 container's default user (`k6`, uid 12345) cannot write to a
  host-mounted `docs/loadtest` directory owned by `root`; the Makefile
  target runs the containers with `-u root` to avoid that permission
  mismatch (acceptable for a local, throwaway load-test container).
- Both runs finished the confirm phase's *first* polling round with 0
  pending — every accepted video was already `DONE` by the time
  `confirm.js` started (about a minute after the spike began, including
  the time to build/start the `confirm.js` container). The 4-slot worker
  pool comfortably drained ~500 one-second fixture videos well inside that
  window; nothing indicated a resource limit or backend bug in this
  sandbox.
