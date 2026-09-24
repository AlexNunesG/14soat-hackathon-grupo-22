# Integration tests

Black-box executable spec of the v1 contract in
[`docs/openapi.yaml`](../../docs/openapi.yaml)
([ADR 0001](../../docs/adr/0001-replace-legacy-test-contract.md)). The tests
only talk to the running system over HTTP: the API and the MailHog inbox. They
never import its code. No mocks: test videos are generated with the real
`ffmpeg` and processed by the real services, and the tests check the real ZIP
and PNG output and the real e-mails caught by MailHog. Each area of the
contract has its own file.

The Ground rules below are copied verbatim from
[`.ai-agents/PLAN.md`](../../.ai-agents/PLAN.md) ("this plan" and "§2" refer
to it); PLAN.md is the source of truth if they diverge.

## Ground rules: the tests are the spec

These rules apply to every phase and every PR, human or AI agent.

1. **The integration tests are the specification.** Once Phase 1 is merged,
   `tests/integration/` (with `docs/openapi.yaml`) defines what the system must
   do. When code and tests disagree, the code is wrong.
2. **The challenge PDF wins over the tests.** The only valid reason to change
   the expected behavior of a test is that it contradicts, or fails to cover,
   a requirement in the PDF (§2). In that case:
   - fix the test in its own PR, separate from implementation code;
   - explain in the PR description which requirement (RF/RT/D id) the test
     got wrong and why;
   - update `docs/openapi.yaml` and this plan in the same PR.
3. **Avoid changing tests.** Do not edit an assertion, timeout, status code or
   payload to make an implementation pass. Allowed without the process above:
   fixing a bug in the harness or a helper that does not change what is
   asserted, and adding *new* tests for uncovered behavior.
4. **Enable tests as features are implemented.** Each implementation PR
   deletes the `notImplemented(t)` line of every test it makes pass — no
   later, no earlier. A test is never enabled without the code that makes it
   pass, and a feature is never merged while its tests are still skipped.
5. **Never skip, disable or weaken a test to get green CI.** Re-adding
   `notImplemented(t)` to an enabled test counts as disabling it.

## Status: enabled as the services are built

Tests whose behavior is not implemented yet start with `notImplemented(t)`,
which skips them. Enabled so far: `health_test.go` (Phase 2.1),
`auth_test.go` (Phase 2.2), `upload_test.go`, `processing_test.go`,
`status_test.go`, `download_test.go`, `concurrency_test.go` and
`resilience_test.go` (Phase 2.3), and `notification_test.go` and
`e2e_test.go` (Phase 2.5): every test is enabled.

To enable a test, delete its `notImplemented(t)` line in the same pull request
that implements the behavior it checks (Ground rule 4). An enabled test fails
with "no app to test" until there is a stack to run against (see below).

To see what is still pending (Phase 2 is done when this prints nothing):

```sh
grep -rn 'notImplemented(t)$' tests/integration/
```

| File | Covers | Requirement |
|---|---|---|
| `health_test.go` | `GET /healthz`, `GET /readyz` (no auth) | — (probes for the stack) |
| `auth_test.go` | Register, duplicate e-mail, invalid input, login, `401` without / with an invalid token | RF3 |
| `upload_test.go` | `POST /api/v1/videos`: `202` + `PENDING`, several files, missing file, unsupported and supported extensions (any case) | RF2, RF3 |
| `processing_test.go` | `PENDING → PROCESSING → DONE \| FAILED`, 1 fps PNG frames, zip layout, every format, undecodable / audio-only / zero-frame videos `FAILED` | RF1, RT1 |
| `status_test.go` | `GET /api/v1/videos` (own videos only, newest first, pagination) and `GET /api/v1/videos/{id}` | RF4, RF3 |
| `download_test.go` | `GET /api/v1/videos/{id}/download`: zip when `DONE`, `409` otherwise, `404` | RF4 |
| `concurrency_test.go` | Several videos `PROCESSING` at once; simultaneous uploads from several users | RF1, RT2 |
| `resilience_test.go` | Burst of concurrent uploads, none lost; worker restart mid-run loses no video | RF2 |
| `notification_test.go` | Failure e-mail to the owner (subject has the file name, body the `error_message`); none for `DONE` | RF5 |
| `e2e_test.go` | Sign up → log in → upload → poll → download → failure e-mail | RF1–RF5 |
| `main_test.go` | Harness: `TestMain`, starting / stopping the compose stack, waiting for `/healthz` | — |
| `api_test.go` | Helpers for the v1 API: schema types, users and tokens, requests, uploads, status polling, error envelope | — |
| `helpers_test.go` | Shared helpers: `notImplemented`, ffmpeg-generated videos, zip / PNG checks | — |
| `mailhog_test.go` | Helpers that read and decode e-mails from the MailHog HTTP API | — |

RT1 (persistence) is exercised by every test, since state must survive across
requests.

## Requirements

- `ffmpeg` in `PATH`: it generates the test videos. Without it the suite
  fails at once.
- For the default mode, `docker` with the compose plugin.

## Running against the compose stack (default)

```sh
make test-integration      # go test -v -race -count=1 ./tests/integration/
```

With `BASE_URL` unset, if the compose file exists (`COMPOSE_FILE`, default
`deploy/docker-compose.yml`, relative to the module root) and `docker` is
available, `TestMain` runs `docker compose -f <file> up -d --build --wait`
before the tests and `docker compose -f <file> down -v` after them. Set
`KEEP_STACK=1` to leave the stack running. The API is expected at
`http://localhost:8080` and MailHog at `http://localhost:8025`.

Without a compose file or `docker`, nothing is started: skipped tests pass and
every enabled test fails with "no app to test".

## Running against a running stack

```sh
BASE_URL=http://localhost:8080 MAILHOG_URL=http://localhost:8025 \
  go test -v -count=1 ./tests/integration/
```

Nothing is started or stopped. `TestWorkerRestartLosesNoVideo` is skipped,
since the suite does not control the stack. The tests don't assume the stack
starts empty: each one registers its own unique users.

In both modes `TestMain` waits up to 120 s for `GET /healthz` to return `200`
before running the tests. If it never does, enabled tests fail with the reason.

## Environment variables

| Variable | Default | Used for |
|---|---|---|
| `BASE_URL` | unset (compose mode, `http://localhost:8080`) | API under test; when set, nothing is started |
| `MAILHOG_URL` | `http://localhost:8025` | MailHog HTTP API read by the notification tests |
| `COMPOSE_FILE` | `deploy/docker-compose.yml` | Compose file started in the default mode |
| `KEEP_STACK` | unset | `1` leaves the compose stack running after the tests |
| `WORKER_SERVICE` | `worker` | Compose service restarted by `TestWorkerRestartLosesNoVideo` |
| `PROCESSING_TIMEOUT` | `3m` | Max wait for a video to reach `DONE` / `FAILED` (Go duration) |
| `BURST_SIZE` | `20` | Concurrent uploads in `TestBurstOfUploadsIsNotLost` |
| `BURST_TIMEOUT` | 2 × `PROCESSING_TIMEOUT` | Max wait for the whole burst to be processed |
| `MAIL_TIMEOUT` | `60s` | Max wait for an expected e-mail |

## What the stack must provide

These come from the tests and `docs/openapi.yaml`:

- **At least 2 concurrent processing slots** (worker replicas × per-worker
  concurrency). With one slot RF1 is not met and
  `TestUploadsAreProcessedInParallel` fails.
- **`PROCESSING` means a worker is actually processing the video.** The
  concurrency and restart tests sample the status to see videos in flight.
- **A compose service named `worker`** (or `WORKER_SERVICE`), restarted by the
  resilience test. A restart may make a video be processed again, never lost.
- **Failure e-mails** go to the address used to register; the subject
  contains the original file name (MIME-encoded when non-ASCII) and the body
  contains the video's `error_message`. A `DONE` video sends no e-mail.

## CI

`make check` runs what CI (`.github/workflows/ci.yml`) runs: `gofmt`,
`go vet`, `golangci-lint` and `go test -race ./...`, which includes this suite
in the default mode: it starts `deploy/docker-compose.yml`, runs the enabled
tests against it and removes the stack.
