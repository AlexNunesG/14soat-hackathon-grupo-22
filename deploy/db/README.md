# deploy/db

SQL scripts placed here are mounted into the PostgreSQL container's
`/docker-entrypoint-initdb.d/` and run automatically on first startup (see
`deploy/docker-compose.yml`).

No schema yet — table definitions (`users`, `videos`/jobs, etc.) land with the services that own
them in later plan items. See [`.ai-agents/PLAN.md`](../../.ai-agents/PLAN.md).
