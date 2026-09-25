# Deployment images

`.github/workflows/publish.yml` builds and publishes the three service
images on every push to `main` (i.e. every merged PR): one job per service,
matrixed over `api`, `worker`, `notifier`. Each leg builds
`deploy/docker/<service>.Dockerfile` with context `.` (the repo root), the
same way `deploy/docker-compose.yml` builds them, and pushes to GHCR:

```
ghcr.io/<owner>/<repo>/api:<sha>
ghcr.io/<owner>/<repo>/api:latest
ghcr.io/<owner>/<repo>/worker:<sha>
ghcr.io/<owner>/<repo>/worker:latest
ghcr.io/<owner>/<repo>/notifier:<sha>
ghcr.io/<owner>/<repo>/notifier:latest
```

`<owner>/<repo>` is `${{ github.repository }}` lower-cased (GHCR requires a
lowercase path). `<sha>` is the full commit SHA that was merged to `main`
(`github.sha`); `latest` always points at the most recent `main` build.

CI (`.github/workflows/ci.yml`) is the required check on pull requests
(lint, vet, unit tests, the full `docker compose` integration suite,
coverage, govulncheck); `publish.yml` only runs after a commit has already
merged to `main`; through branch protection it has already passed CI on its
PR, so `publish.yml` does not re-run the test suite — it only builds and
pushes images.

## Pulling and running a published image

Each image is a drop-in replacement for the one `docker compose` builds
locally: same entrypoint, same environment variables (`.env.example`), same
ports. To run a service against an already-running local stack (`make up`,
which also builds `migrate`/`api`/`worker`/`notifier` locally) without
building it yourself, override just that service's `image` and drop its
`build:` block, e.g. with a compose override file:

```yaml
# deploy/docker-compose.override.yml (not committed)
services:
  api:
    image: ghcr.io/<owner>/<repo>/api:latest
  worker:
    image: ghcr.io/<owner>/<repo>/worker:latest
  notifier:
    image: ghcr.io/<owner>/<repo>/notifier:latest
```

```sh
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.override.yml up -d --wait
```

Or run a single image directly, supplying the same variables the compose
service sets (see `.env.example` and the `environment:` block of that
service in `deploy/docker-compose.yml`):

```sh
docker pull ghcr.io/<owner>/<repo>/api:latest
docker run --rm -p 8080:8080 \
  -e DATABASE_URL=postgres://video:video@<postgres-host>:5432/video_processor?sslmode=disable \
  -e AMQP_URL=amqp://video:video@<rabbitmq-host>:5672/ \
  -e S3_ENDPOINT=<storage-host>:8333 \
  -e S3_ACCESS_KEY=dev-access-key -e S3_SECRET_KEY=dev-secret-key \
  -e JWT_SECRET=<32+ byte secret> \
  ghcr.io/<owner>/<repo>/api:latest
```

By default GHCR packages created from a repository inherit that
repository's visibility; a private repo's packages need their own
visibility set to public (package settings on GHCR) if they should be
pullable without authentication. `README.md` (Phase 7) covers running the
full stack locally with `make up`.
