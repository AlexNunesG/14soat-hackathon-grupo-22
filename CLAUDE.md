# CLAUDE.md

Guidance for AI agents and contributors working in this repo.

## Project

FIAP X Video Processor (SOAT Phase 5 hackathon): users upload videos and later
download a `.zip` of their frames. The legacy single-process demo is being
rebuilt as Go microservices — **api**, **worker**, **notifier** — on
PostgreSQL, Redis, RabbitMQ, MinIO, MailHog and Prometheus/Grafana.

- Plan and living checklist: [`.ai-agents/PLAN.md`](.ai-agents/PLAN.md).
  Tick items (`- [x]`) in the same PR that delivers them and keep the
  requirement traceability table in sync.
- Decisions: [`docs/adr/`](docs/adr/). Integration suite:
  [`tests/integration/`](tests/integration/).
- The Ground rules below are copied verbatim from PLAN.md ("this plan" and
  "§2" refer to it); PLAN.md is the source of truth if they diverge.

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

## Repo layout (ADR 0002)

Single Go module (root `go.mod`), one binary per service:

```
cmd/{api,worker,notifier}   thin main packages (wiring only)
internal/domain             entities, JobStatus, format validation
internal/app                use cases and ports
internal/adapters/          http, postgres, rabbitmq, storage, ffmpeg, zip, mailer
internal/platform/          config, logging
db/migrations/              versioned SQL
deploy/                     compose, k8s, RabbitMQ definitions
docs/                       architecture, ADRs, openapi.yaml
tests/integration/          black-box suite (the spec)
```

`internal/domain` imports nothing from the project; adapters implement ports
and don't import each other; `cmd/*` holds no business logic.

## Before pushing

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...   # needs ffmpeg in PATH
```
