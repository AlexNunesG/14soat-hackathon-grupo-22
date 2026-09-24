# Architecture Decision Records

Short records of decisions that shape the project. One file per decision,
numbered in order (`NNNN-short-title.md`). An accepted ADR is not edited to
change its decision: write a new ADR that supersedes it and update the old
one's status.

| ADR | Title | Status |
|---|---|---|
| [0001](./0001-replace-legacy-test-contract.md) | Replace the legacy test contract | Accepted |
| [0002](./0002-repository-layout.md) | Repository layout: single-module monorepo | Accepted |

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
