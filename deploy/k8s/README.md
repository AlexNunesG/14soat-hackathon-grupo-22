# Kubernetes manifests (Phase 4.3)

Kustomize (bundled with `kubectl`, no extra tooling) layout:

```
deploy/k8s/
├── base/
│   ├── namespace/         Namespace "video-processor"
│   ├── config/            app.env (ConfigMap source) + secrets.env.example (Secret source, placeholders)
│   ├── infra/              postgres, redis, rabbitmq, storage (SeaweedFS), mailhog
│   ├── app/                migrate Job, api, worker, notifier Deployments + Services
│   └── policy/              HPA, PodDisruptionBudget, NetworkPolicy
├── overlays/
│   ├── dev/                single replica, no HPA, IfNotPresent images (kind/local)
│   └── prod/                more replicas, HPA on, tuned resources, real secrets
├── keda/                   opt-in ScaledObject: worker scales on video.process queue depth
└── README.md               this file
```

Every service's environment variable comes straight from
`internal/platform/config` / `.env.example`; the ConfigMap/Secret keys here
use the exact same names, so nothing in the Go code changes for this PR.

## Config and secrets

- **`app-config`** (ConfigMap, `base/config/app.env`): every non-secret
  variable shared by api/worker/notifier (`LOG_LEVEL`, timeouts,
  `S3_BUCKET`/`S3_REGION`/`S3_USE_SSL`, `CACHE_TTL`, outbox settings,
  `WORKER_*`, `NOTIFIER_*`, SMTP host/port/from/tls, `APP_URL`). Per-service
  listen addresses (`HTTP_ADDR`, `METRICS_ADDR`, `HEALTH_ADDR`) are set
  directly on each Deployment instead, exactly like compose fixes them.
- **`app-secrets`** (Secret, via `secretGenerator`): `DATABASE_URL`,
  `AMQP_URL`, `JWT_SECRET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`,
  `SMTP_USERNAME`/`SMTP_PASSWORD`, plus the values the in-cluster
  Postgres/RabbitMQ StatefulSets use to create themselves
  (`POSTGRES_USER/PASSWORD/DB`, `RABBITMQ_USER/PASSWORD`).
  `base/config/secrets.env.example` holds **placeholder, development-only**
  values (identical to `.env.example`'s) and is committed so the base and
  `overlays/dev` apply out of the box for local (kind) testing — same spirit
  as compose's built-in `${VAR:-default}`.
- `overlays/prod` **replaces** that generator with one reading
  `overlays/prod/secrets.env`, a **gitignored** file (`deploy/k8s/.gitignore`)
  that does not exist until you create it:
  ```sh
  cp deploy/k8s/base/config/secrets.env.example deploy/k8s/overlays/prod/secrets.env
  $EDITOR deploy/k8s/overlays/prod/secrets.env   # real credentials; JWT_SECRET via `openssl rand -base64 48`
  ```
  `kubectl kustomize`/`apply -k overlays/prod` fail until that file exists —
  deliberately, so a real credential can only end up in the one place meant
  to hold it. For an actual production cluster, prefer swapping this whole
  mechanism for your cluster's secret manager (Sealed Secrets, External
  Secrets Operator, SOPS, the cloud provider's secret store): a Kubernetes
  `Secret` is only base64-encoded, not encrypted at rest by default.
  **Never commit real credentials to either `secrets.env` file.**

## Infra: in-cluster today, managed services later (RT2 evidence)

`base/infra/*` runs Postgres, Redis, RabbitMQ, SeaweedFS (S3) and MailHog
*inside* the cluster (StatefulSets with PVCs for Postgres/RabbitMQ/storage,
plain Deployments for Redis/MailHog) — reasonable for a hackathon
deployment with no managed cloud services assumed, and it mirrors
`deploy/docker-compose.yml` image-for-image.

For a real deployment, swap these for managed services and point the same
`app-config`/`app-secrets` keys at them:

| in-cluster (`base/infra/`) | managed equivalent | what changes |
|---|---|---|
| `postgres` StatefulSet | RDS / Cloud SQL | `DATABASE_URL` in `app-secrets`; delete `infra/postgres` |
| `redis` Deployment | ElastiCache / Memorystore | `REDIS_URL` in `app-config`; delete `infra/redis` |
| `rabbitmq` StatefulSet | Amazon MQ / CloudAMQP | `AMQP_URL` in `app-secrets`; delete `infra/rabbitmq` |
| `storage` (SeaweedFS) | AWS S3 / MinIO | `S3_ENDPOINT`/`S3_*` keys; delete `infra/storage` |
| `mailhog` Deployment | a real SMTP provider | `SMTP_HOST/PORT/USERNAME/PASSWORD/TLS`; delete `infra/mailhog` |

None of this touches Go code: `internal/adapters/{postgres,rabbitmq,storage,mailer}`
only ever see a URL/endpoint and credentials from config — this is exactly
what the ports/adapters architecture (ADR 0002) buys.

## Applying

### 1. Local testing with `kind`

```sh
kind create cluster --name videoproc

# Build the same images compose builds, then load them into kind's node
# (no registry needed — imagePullPolicy: IfNotPresent in overlays/dev).
docker build -f deploy/docker/api.Dockerfile      -t video-processor/api:dev      .
docker build -f deploy/docker/worker.Dockerfile   -t video-processor/worker:dev   .
docker build -f deploy/docker/notifier.Dockerfile -t video-processor/notifier:dev .
kind load docker-image video-processor/api:dev video-processor/worker:dev video-processor/notifier:dev --name videoproc

# metrics-server, for HPA to show real numbers (kind has none by default):
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

### 2. Apply, then run the migration

```sh
kubectl apply -k deploy/k8s/overlays/dev
kubectl -n video-processor wait --for=condition=available deploy -l app.kubernetes.io/component=infra --timeout=180s
kubectl -n video-processor wait --for=condition=complete job/migrate --timeout=120s
kubectl -n video-processor wait --for=condition=available deploy/api deploy/worker deploy/notifier --timeout=180s
```

`migrate` is a plain `batch/v1` Job (`base/app/migrate/job.yaml`) running
`api migrate`, applied by `kubectl apply -k` alongside everything else — the
simplest correct option for a hackathon (no Helm hooks, no init containers
racing each other). It is **not** an init container on api/worker/notifier:
migrations must run exactly once, not once per pod/replica. Re-run it with:

```sh
kubectl -n video-processor delete job/migrate --ignore-not-found
kubectl apply -k deploy/k8s/overlays/dev
```

### 3. Reach the API

```sh
kubectl -n video-processor port-forward svc/api 8080:8080
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
```

### 4. Production-shaped overlay

```sh
cp deploy/k8s/base/config/secrets.env.example deploy/k8s/overlays/prod/secrets.env
$EDITOR deploy/k8s/overlays/prod/secrets.env
kubectl kustomize deploy/k8s/overlays/prod        # render and review first
kubectl apply -k deploy/k8s/overlays/prod
```

`overlays/prod` sets `api`/`worker` replicas up, turns `imagePullPolicy` to
`Always` (point `images:` at your registry first — see the comment in
`overlays/prod/kustomization.yaml`), tunes resource requests/limits, and
widens the HPAs' `minReplicas`/`maxReplicas`.

## HPA and KEDA (RT2)

`base/policy/hpa.yaml` has `autoscaling/v2` HorizontalPodAutoscalers for
`api` and `worker`, CPU-based (target 70% utilization). `overlays/dev`
removes both (a single replica has nothing to autoscale, and dev clusters
may not have metrics-server); `overlays/prod` keeps them and widens their
bounds.

`deploy/k8s/keda/` is an **opt-in stretch goal**: a `ScaledObject` that
scales `worker` on the depth of the `video.process` RabbitMQ queue instead
of CPU — a much more direct signal for a queue-consuming worker. It needs
the [KEDA operator](https://keda.sh) installed separately (not part of this
repo):

```sh
helm repo add kedacore https://kedacore.github.io/charts
helm install keda kedacore/keda --namespace keda --create-namespace
kubectl apply -k deploy/k8s/keda
```

When KEDA's ScaledObject is applied, remove or disable the CPU-based
`worker` HPA (`base/policy/hpa.yaml`) — KEDA creates and owns its own HPA
for the same Deployment, and two HPAs on one Deployment fight each other.

## Security, availability

- Every container runs as the same non-root UID the Dockerfiles create
  (`10001:10001`), `allowPrivilegeEscalation: false`,
  `capabilities: { drop: ["ALL"] }`, and `readOnlyRootFilesystem: true`
  where the process needs no writable root (api/worker mount an `emptyDir`
  at `/tmp` for uploads/job files; the infra images keep their upstream
  filesystem needs, e.g. Postgres/RabbitMQ's data directories, which are
  PVC-backed anyway).
- `PodDisruptionBudget`s (`minAvailable: 1`) protect `api` and `worker` from
  voluntary disruptions (node drains, cluster upgrades) taking every replica
  down at once.
- `NetworkPolicy` (`base/policy/networkpolicy.yaml`) denies ingress by
  default and re-opens it only within the namespace, plus unrestricted
  ingress to `api` (the public surface). It needs a CNI that enforces
  `NetworkPolicy` (Calico, Cilium, ...); kind's default `kindnet` does not,
  so it has no *effect* there, but it applies and validates cleanly.
- `livenessProbe`/`readinessProbe` mirror `internal/adapters/http/health.go`
  and the compose healthchecks: `api` uses `GET /healthz` for liveness only
  (to avoid a flapping dependency taking it out of rotation) and
  `GET /readyz` for readiness; `worker`/`notifier` use the same two routes
  on their internal `HEALTH_ADDR` (`:8081`).

## Validation performed for this PR

- `kubectl kustomize deploy/k8s/overlays/{dev,prod}` and
  `kubectl kustomize deploy/k8s/keda` all render without error (29 objects
  each for `base`/`dev`/`prod`).
- Every rendered manifest validated against the Kubernetes 1.31 schema with
  [`kubeconform`](https://github.com/yannh/kubeconform): `dev` 29/29 valid,
  `prod` 29/29 valid (KEDA's two CRD kinds are not in its bundled schema set
  and were skipped; `kubectl`'s own server-side validation is authoritative
  for those once KEDA is installed).
- All three service images (`docker build -f deploy/docker/*.Dockerfile`,
  matching the compose build exactly) built successfully.
- **Not validated in this session:** a live `kind` cluster. `kind create
  cluster` hangs indefinitely at `Starting control-plane`, in this sandboxed
  session only. Root cause, isolated with `crictl runp` on minimal sandbox
  configs: `kubeadm`'s own static pods (etcd, kube-apiserver,
  kube-controller-manager, kube-scheduler) all set `hostNetwork: true`, and
  the pause-container sandbox for a pod that joins the *host* network
  namespace fails at `runc create` (`can't get final child's PID from pipe:
  EOF`) inside this environment's nested Docker-in-Docker-in-sandbox stack;
  the identical call with a fresh network namespace succeeds. This is a
  limitation of the sandbox that ran this PR's validation, not of the
  manifests in `deploy/k8s/` — none of them set `hostNetwork` (only
  kubeadm's own bootstrap pods do, and that is outside this directory
  entirely). As a result, **pods becoming `Ready`, `GET /healthz` /
  `GET /readyz` via port-forward, and an end-to-end upload-to-`DONE` smoke
  test have not been exercised against a real cluster** and should be run
  once before relying on this for a demo: `kind create cluster`, build +
  `kind load docker-image` the three images, `kubectl apply -k
  overlays/dev`, then `kubectl wait --for=condition=ready pod --all -n
  video-processor --timeout=5m` and a port-forward + curl.

## Left for a future PR

- Prometheus/Grafana are not deployed to `deploy/k8s/` in this PR (only
  Deployments/Services/ConfigMaps/Secrets/HPA for api and worker were in
  scope here, per `.ai-agents/PLAN.md` Phase 4.3); the `prometheus.io/scrape`
  annotations are already on the api/worker/notifier pod templates so a
  Prometheus with Kubernetes service discovery (or the kube-prometheus-stack
  Helm chart) picks them up with no further changes.
- An Ingress (or Gateway API) resource for the api, instead of
  `port-forward`/`NodePort`, once a specific ingress controller is chosen.
  `api-allow-ingress` in `base/policy/networkpolicy.yaml` already allows
  ingress from anywhere, ready for one to sit in front.
- Wiring this into CI/CD (Phase 6): `kubectl apply -k deploy/k8s/overlays/prod`
  as the CD job's last step, after the image build/push.
