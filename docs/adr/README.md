# Architecture Decision Records

Short records of decisions that shape the project. One file per decision,
numbered in order (`NNNN-short-title.md`). An accepted ADR is not edited to
change its decision: write a new ADR that supersedes it and update the old
one's status.

| ADR | Title | Status |
|---|---|---|
| [0001](./0001-replace-legacy-test-contract.md) | Replace the legacy test contract | Accepted |
| [0002](./0002-repository-layout.md) | Repository layout: single-module monorepo | Accepted |
| [0003](./0003-object-storage-seaweedfs.md) | Object storage: S3 API, SeaweedFS locally | Accepted |
| [0004](./0004-transactional-outbox.md) | Transactional outbox for processing jobs | Accepted |
| [0005](./0005-quorum-queues-delivery-limit.md) | Quorum queues with x-delivery-limit for video.process and video.notify | Accepted |

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context

What problem we face and which requirements (RF/RT/D ids) it touches.

## Decision

What we will do.

## Consequences

What becomes easier or harder, and what follow-up work it creates.
```
