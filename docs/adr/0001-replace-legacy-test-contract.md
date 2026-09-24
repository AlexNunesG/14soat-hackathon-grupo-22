# 0001. Replace the legacy test contract

- Status: Accepted
- Date: 2026-09-24

## Context

`tests/integration/` holds 28 black-box HTTP tests that describe the
**legacy** demo app: `GET /`, synchronous `POST /upload`, `GET /api/status`,
`GET /download/:filename`, public `/uploads/*` and `/outputs/*`, CORS and
process startup/shutdown. All of them are skipped with `notImplemented(t)`.

The hackathon rebuild changes that contract on purpose: authentication (RF3),
asynchronous processing through a queue (RF1, RF2), per-user status (RF4) and
failure notifications (RF5). Keeping the legacy tests would either block the
new design or force us to implement behavior nobody needs (e.g. a failed
processing returning HTTP 200, public uploads).

## Decision

The legacy contract is dropped. The work is split in two ordered phases
(see [`.ai-agents/PLAN.md`](../../.ai-agents/PLAN.md) §5 and §6):

1. **Phase 1 — Rebuild the tests.** Rewrite `tests/integration/` for the v1
   behavior (v1 API, auth, async processing, per-user status, notifications),
   with the contract written in `docs/openapi.yaml`. Legacy tests are
   deleted. Every new test starts with `notImplemented(t)`, so the suite stays
   green and doubles as the executable spec. No production code in this phase.
2. **Phase 2 — Implement.** Build the services until the new tests pass. Each
   implementation PR deletes the `notImplemented(t)` line of every test it
   makes pass. The phase is done when
   `grep -rn 'notImplemented(t)$' tests/integration/` returns nothing.

After Phase 1 the suite follows the **Ground rules: the tests are the spec**
in PLAN.md. In summary:

- The integration tests (with `docs/openapi.yaml`) are the specification;
  when code and tests disagree, the code is wrong.
- The challenge PDF wins over the tests. A test's expected behavior changes
  only if it contradicts or misses a PDF requirement, in its own PR that
  names the RF/RT/D id and updates `docs/openapi.yaml` and the plan.
- Avoid changing tests. Harness/helper bug fixes that don't change what is
  asserted, and new tests, are fine.
- Enable a test in the same PR that implements its behavior — no earlier,
  no later.
- Never skip, disable or weaken a test to get green CI (re-adding
  `notImplemented(t)` counts as disabling).

## Consequences

- The suite describes the target system before any code exists, so agents and
  humans implement against a fixed, reviewable spec.
- Legacy-only checks (CORS on every route, `/uploads/*`, startup flags) are
  lost as integration tests; startup and graceful shutdown move to
  per-service unit tests in Phase 2.
- The harness must change to target the compose stack (`BASE_URL`,
  `MAILHOG_URL`) instead of building a single binary from the module root.
- The pending-work metric is simple and objective: the count of
  `notImplemented(t)` lines.
- The Ground rules are copied into `tests/integration/README.md` (Phase 1.4)
  and the root `CLAUDE.md`.
