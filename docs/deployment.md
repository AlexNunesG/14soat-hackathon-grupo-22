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

## CD: deploying to Kubernetes

`.github/workflows/deploy.yml` (job `deploy-k8s`) applies
`deploy/k8s/overlays/prod` (see `deploy/k8s/README.md`) to whatever cluster
`KUBE_CONFIG` points at, right after `publish.yml` finishes pushing images
for a commit on `main`.

**Trigger.** `workflow_run` watching `Publish`, `types: [completed]`,
filtered in the job's own `if:` to `conclusion == 'success'` (a failed or
cancelled publish must not trigger a deploy of stale images) — not a
same-file `needs: publish` job inside `publish.yml`. `workflow_run` is the
standard way to run something after another workflow finishes; it keeps the
two workflows' concerns separate (build vs. ship) and lets `deploy.yml` be
re-triggered on its own (`workflow_dispatch`, e.g. to redeploy after a
rollback) without forcing a rebuild, which a `needs:` job in the same file
cannot do. The gotcha: a `workflow_run` run executes the *default branch's*
copy of `deploy.yml` and its own `github.sha`/`ref` default to the default
branch, not the commit that was published — the workflow checks out and
tags everything from `github.event.workflow_run.head_sha` explicitly to
stay pinned to the exact commit `publish.yml` built.

**Required secrets** (repo → Settings → Secrets and variables → Actions;
scope them to the `production` GitHub Environment below if you create one):

- **`KUBE_CONFIG`** — base64-encoded kubeconfig for the target cluster
  (a service-account/context with permission to apply into the
  `video-processor` namespace is enough; it does not need cluster-admin).
  Produce it with:
  ```sh
  cat ~/.kube/config | base64 -w0
  ```
  For a cloud-managed cluster, generate a kubeconfig scoped to a
  deploy-only identity first (e.g. `aws eks update-kubeconfig` /
  `gcloud container clusters get-credentials` against a limited
  service account) rather than reusing your personal admin config.
- **`PROD_SECRETS_ENV`** — the contents `deploy/k8s/overlays/prod/secrets.env`
  would normally hold locally (see `deploy/k8s/README.md`, "Config and
  secrets"): that file is gitignored and deliberately cannot exist in a
  fresh CI checkout, so the workflow reconstructs it from this one
  multi-line secret before running kustomize. Its value must be plain
  `KEY=VALUE` lines, one per line, with **exactly** the keys
  `deploy/k8s/base/config/secrets.env.example` declares — real,
  non-placeholder values:
  ```
  POSTGRES_USER=...
  POSTGRES_PASSWORD=...
  POSTGRES_DB=...
  DATABASE_URL=postgres://...
  RABBITMQ_USER=...
  RABBITMQ_PASSWORD=...
  RABBITMQ_ERLANG_COOKIE=...
  AMQP_URL=amqp://...
  S3_ACCESS_KEY=...
  S3_SECRET_KEY=...
  JWT_SECRET=...          # openssl rand -base64 48
  SMTP_USERNAME=...
  SMTP_PASSWORD=...
  ```
  Paste it into the GitHub secret value as-is (multi-line secrets are
  supported). If you swap `overlays/prod`'s in-cluster Postgres/RabbitMQ for
  managed services (README's "Going to managed services" table), update
  `DATABASE_URL`/`AMQP_URL` here accordingly and the `POSTGRES_*`/
  `RABBITMQ_*` keys become unused (harmless to leave, since nothing reads
  them once `infra/postgres`/`infra/rabbitmq` are removed from the overlay).

If either secret is missing, the job fails fast on its "Check required
secrets" step with an `::error::` annotation naming exactly what to add —
it does **not** silently skip and report success.

**Image pinning.** The job pins the three images to the commit SHA that was
just published (never `:latest`, for a reproducible rollout) by layering a
small, CI-only kustomize overlay on top of `overlays/prod` with its own
`images:` transformer (`newName`/`newTag` set to
`ghcr.io/<owner>/<repo>/<service>:<sha>`); it does not modify any committed
file. (`kubectl kustomize` has no `edit` subcommand — that belongs to the
standalone `kustomize` CLI, which GitHub-hosted runners don't ship — so the
workflow uses kustomize's declarative `images:` composition instead of
`kustomize edit set image`.)

**Smoke check and rollback.** After `kubectl apply -k`, the job runs
`kubectl rollout status` for `api`, `worker` and `notifier` (each up to
120s); this already exercises the real `/healthz`/`/readyz` readiness
probes before the deploy is considered successful, which is a simpler and
still meaningful check than standing up a one-off in-cluster curl pod for a
hackathon-scale pipeline. If rollout status ever fails, an `if: failure()`
step runs `kubectl rollout undo` on all three Deployments as a low-risk
safety net (it only touches those three Deployments, never infra or data)
so a bad deploy doesn't need a human to page in during a demo.

**One-time manual setup for a repo admin** (outside this repo's files,
done in GitHub's Settings UI):

1. Add the `KUBE_CONFIG` and `PROD_SECRETS_ENV` repository secrets above.
2. Optionally create a GitHub Environment named `production` (Settings →
   Environments) — `deploy-k8s` already declares `environment: production`,
   so once that Environment exists you can attach required reviewers,
   wait timers or environment-scoped secrets to it without touching the
   workflow file. This is optional; the job runs against the repo-level
   secrets either way if no such Environment is configured.
3. Nothing else — `deploy.yml` runs automatically after the next
   successful `Publish` run on `main`, or on demand via
   Actions → Deploy → **Run workflow**.

## CD alternative: compose on a VM

A team without a Kubernetes cluster can deploy the same published images to
a single VM with `docker compose` over SSH instead — equally valid for this
project, just not wired up in this repo. The shape (e.g. with
[`appleboy/ssh-action`](https://github.com/appleboy/ssh-action)), gated the
same way as `deploy-k8s` (skip cleanly if secrets are absent):

```yaml
- uses: appleboy/ssh-action@v1
  with:
    host: ${{ secrets.SSH_HOST }}
    username: ${{ secrets.SSH_USER }}
    key: ${{ secrets.SSH_KEY }}
    script: |
      cd /opt/video-processor
      docker compose -f deploy/docker-compose.yml pull
      docker compose -f deploy/docker-compose.yml up -d --wait
```

Required secrets: `SSH_HOST` (the VM's address), `SSH_USER` (a user with
docker access), `SSH_KEY` (a private key authorized on that VM). The VM
needs `deploy/docker-compose.yml` and a `.env` with real credentials
checked out or copied ahead of time (the `git pull`/`scp` step is left to
whoever wires this up), and image references pointed at
`ghcr.io/<owner>/<repo>/<service>:<sha or latest>` the same way
`docker-compose.override.yml` does above. This repo does not implement this
workflow: it adds real operational surface (a standing SSH key, a
long-lived VM) that a hackathon deployment doesn't need when a cluster is
available, and the Kubernetes path above already covers "CD after publish"
for this PR.
