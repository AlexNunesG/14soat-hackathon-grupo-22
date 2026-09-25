# Presentation video — script and outline (deliverable D4)

Target: **≤ 10 minutes**, covering documentation → architecture → the
project working, per `.ai-agents/PLAN.md`'s Phase 7 checklist. This is the
**script and shot list only** — recording and uploading the video, and
adding its link here and in the README, are separate, manual steps for a
human to do (screen recording and video hosting aren't something this
session can produce).

Suggested recording setup: a terminal (this repo checked out, stack not yet
started) + a browser with two tabs (the web UI, and Grafana) + this script
open on a second screen or printed. Run `make up` a few minutes before
recording so image pulls/builds don't eat into the 10 minutes — `make down
&& make up` right before recording instead, for a clean state, if time
allows; otherwise `make up` alone is idempotent and fine to leave running
from an earlier session.

Total budget: **10:00**. Segment timings are a guide, not a hard rule — the
demo (§3) is the part worth protecting if something runs long elsewhere.

---

## 1. Documentation (≈ 1:30)

**Say:** "This is the FIAP X Video Processor, the SOAT Phase 5 hackathon
challenge — rebuild a single-process video-to-frames demo as a proper,
production-shaped system: microservices, messaging, and everything else the
course covered."

**Show:**
- The challenge PDF in `.ai-agents/` for 2 seconds, just to establish the
  source requirement.
- `README.md` — scroll through the section headers only (don't read
  paragraphs on camera): overview, run-it-locally, API examples, docs index.
  Say: "The README is the front door — one command to run it, curl examples
  for the whole API, and an index of everything else."
- `.ai-agents/PLAN.md`'s requirement traceability table (§7) — this is the
  single most useful thing to show a grader. Say: "Every requirement from
  the PDF maps to a test and an implementation step here, and this table is
  the up-to-date record of what's actually done."

## 2. Architecture (≈ 2:30)

**Say:** "Three Go services — api, worker, and notifier — sharing one
domain, talking through Postgres, Redis, RabbitMQ, and S3-compatible
storage."

**Show, in `docs/architecture.md`:**
- §1–2: the context and container diagrams. Say: "Uploads go to the api,
  which stores the file and the intent to process it in the same database
  transaction — that's the transactional outbox — then a background relay
  publishes to RabbitMQ. Workers pick jobs up from there, so more than one
  video processes at once, and accepting an upload never depends on a
  worker being free."
- §3, briefly: name the five ADRs on screen (repo layout, object storage,
  outbox, quorum queues, tests-as-spec) without reading them — "each of
  these is a short, real decision record in `docs/adr/`, not just a comment
  in code."
- §4a (the sequence diagram) — trace it with a finger/cursor while saying
  the upload → outbox → queue → worker → done → download path out loud;
  this is the clearest 30 seconds to explain the whole system.
- §4c briefly: "and if a worker crashes on every attempt at one job — not a
  hypothetical, we reproduced it — RabbitMQ's own delivery limit gives up
  and a small reconciler still marks it failed and notifies the owner, so
  nothing gets stuck silently."

## 3. The project working — live demo (≈ 4:30, the core of the video)

Run these against the **already-running** stack (`make up` beforehand).

1. **Web UI** (≈ 1:00): open `http://localhost:8080/`, register a user on
   camera, log in. Upload 2–3 real short videos at once, including one
   corrupt/unsupported file if convenient, to show both the success and
   failure paths. Point out the status table updates live via polling:
   `PENDING → PROCESSING → DONE`, and the failed one shows its error.
   Download a finished zip and open it to show real PNG frames.

2. **Multiple workers / parallel processing — RF1** (≈ 1:00): in a
   terminal, `docker compose -f deploy/docker-compose.yml up -d --scale
   worker=4` while 4+ videos are mid-upload/processing (upload a small
   burst first, e.g. 6–8 videos, so there's a queue to drain). Switch to
   the Grafana dashboard (`http://localhost:3000`, no login needed), the
   "Processing (RF1, RF2)" row — show "Jobs in progress per worker"
   stepping up as replicas join, and "Worker replicas up" ticking to 4.

3. **Status listing and download** (≈ 0:30): back in the UI or via a couple
   of `curl` calls from the README, show `GET /api/v1/videos` returning
   only that user's videos, and a `GET .../download` on a `DONE` video.

4. **Failure e-mail — RF5** (≈ 0:30): open MailHog at
   `http://localhost:8025` and show the failure e-mail that arrived for the
   corrupt upload from step 1 — subject has the file name, body has the
   error.

5. **Grafana dashboard tour** (≈ 1:00): stay on Grafana, scroll the rest of
   the dashboard — API throughput/latency, the overview stat tiles, cache
   hit ratio, runtime (CPU/heap/goroutines per instance). Mention the alert
   rules briefly ("12 rules — DLQ not empty, error ratios, no consumers —
   checked in CI with `make obs-check`").

6. **CI run — RT4/RT5** (≈ 0:30): switch to the GitHub Actions tab for the
   repo, open the latest green run on `main`. Point at the job steps
   scrolling by: lint, the full compose-backed integration suite, coverage,
   `govulncheck`. Mention: "35 integration tests, the whole suite is the
   spec — code that disagrees with a test is what's wrong, not the other
   way around."

## 4. Closing (≈ 0:30)

**Say:** "That's the system — async processing so a spike never loses a
request, evidenced by a k6 load test at 499 out of 499 accepted and
confirmed; observability with Prometheus, Grafana and structured logs; CI
that gates every merge and publishes images; and a Kubernetes deployment
path for when this needs to run for real. Everything shown here, plus what
isn't — the load test results, every ADR, the full API contract — is linked
from the README."

End on the README or the traceability table.

---

## Shot list (quick reference)

| # | Time | Screen | Key point |
|---|---|---|---|
| 1 | 0:00–0:20 | PDF / README title | What this is |
| 2 | 0:20–1:30 | README + PLAN.md §7 | Requirements, how to run/test |
| 3 | 1:30–2:30 | architecture.md §1–2 | Containers, the async design |
| 4 | 2:30–3:30 | architecture.md §3–4a | Why + the upload sequence |
| 5 | 3:30–4:00 | architecture.md §4c | The crash-loop fix |
| 6 | 4:00–5:00 | Web UI | Register, upload, watch status, download |
| 7 | 5:00–6:00 | Terminal + Grafana | Scale workers, watch parallelism live |
| 8 | 6:00–6:30 | UI/curl | List + download |
| 9 | 6:30–7:00 | MailHog | Failure e-mail |
| 10 | 7:00–8:00 | Grafana | Full dashboard tour |
| 11 | 8:00–8:30 | GitHub Actions | A green CI run |
| 12 | 8:30–9:00 | README | Closing |

Leaves a ~1-minute buffer under the 10-minute limit.

## After recording

1. Upload the video (YouTube unlisted, Google Drive, or whatever the
   submission process specifies).
2. Add the link to `README.md` (a "Demo video" line near the top) and tick
   the two remaining Phase 7 boxes in `.ai-agents/PLAN.md` (§7 item, and the
   D4 row in the requirement traceability table).
