# Cache (Redis)

`GET /api/v1/videos` (RF4) is served from Redis when it can be. Clients poll
this list while their videos are processed, so the cache takes load off
Postgres. Correctness comes before hit rate: once a change is committed and
its invalidation has run, no reader gets the old page.

Code: port `app.VideoListCache` / `app.ListInvalidator`
(`internal/app/ports.go`), used by `app.Videos.List`, `app.Uploads` and
`app.Processor`; Redis adapter in `internal/adapters/redis` (go-redis v9).

## Keys

| Key | Value | TTL |
|---|---|---|
| `videos:ver:<owner id>` | the version of the owner's list (an integer) | 24 h, reset by every bump |
| `videos:list:<owner id>:<version>:<page>:<page_size>` | one page as JSON (items and total) | `CACHE_TTL` (default 30 s) |

## Reads and invalidation

- **Read** (api, `GET /api/v1/videos`): read the owner's version, then
  `GET` the page key at that version. On a hit the page is returned. On a
  miss, Postgres is queried and the page is stored under the version read
  *before* the query.
- **Invalidate** (bump the version): the api after an upload's transaction
  commits, before it answers `202`. The worker after each status change
  commits (`PROCESSING`, `DONE`, `FAILED`, including a `FAILED` recorded
  when retries run out).

Pages are never deleted: a bump makes every page of the old version
unreachable, and those pages expire after their TTL. This avoids the race
between a reader filling the cache and a writer invalidating it. A reader
that queried Postgres just before a change stores its old page under the old
version, which no one reads after the bump.

Both operations are small Lua scripts, so they are atomic. A missing version
(a new user, an expired or evicted key) is seeded with the current time in
nanoseconds rather than 0 or 1. A seed is unique and larger than any earlier
version, so pages cached under a lost version are never read again.

`GET /api/v1/videos/{id}` and the download are not cached: they are cheap
primary-key reads, and the tests poll them to wait for a status.

## Failure behavior

The cache is best effort and never takes part in correctness or
availability:

- `REDIS_URL` empty: no cache. The api and the worker run exactly as
  before.
- Redis unreachable or failing: every cache error is logged (`warn`) and
  the api reads Postgres. Uploads and processing never fail because of
  Redis. Timeouts are short (500 ms dial, 300 ms read/write, one retry), so
  an outage costs little latency.
- Redis is not a `/readyz` check. The contract lists exactly `database`,
  `broker` and `storage`.
- An invalidation can be lost while Redis is unreachable from one service
  but not from the other, or when Redis comes back with its data after a
  short outage. A cached page can then be stale for at most `CACHE_TTL`,
  so keep the TTL short.

## Configuration

| Variable | Service | Default | Meaning |
|---|---|---|---|
| `REDIS_URL` | api, worker | empty (compose: `redis://redis:6379/0`) | `redis://` or `rediss://` URL; empty disables the cache |
| `CACHE_TTL` | api | `30s` | Lifetime of a cached page |

## Seeing it work

```sh
make up
docker compose -f deploy/docker-compose.yml exec redis redis-cli MONITOR
# List twice: the first call GETs the page key (miss) and SETs it,
# the second only GETs it (hit). An upload or a status change runs the bump
# script, and the next list misses at the new version.
```

With `LOG_LEVEL=debug` the api also logs `video list served from cache`.
