# 0003. Object storage: S3 API, SeaweedFS locally

- Status: Accepted
- Date: 2026-09-24

## Context

Uploaded videos and generated zips must live outside the API and worker
containers so both can scale horizontally (RT2) and survive restarts (RT1).
PLAN.md §4 proposed MinIO. MinIO no longer publishes community container
images (`minio/minio` tags are gone from Docker Hub and the Bitnami image
was withdrawn), so it cannot back the local compose stack or CI.

## Decision

- The application talks **plain S3** through `github.com/minio/minio-go/v7`
  (path-style addressing, fixed region), behind the `app.ObjectStorage`
  port. Nothing in the code depends on a specific S3 server.
- The compose stack runs **SeaweedFS** (`chrislusf/seaweedfs`, pinned tag)
  in `weed mini` mode as the S3 server, service `storage`. The bucket
  (`S3_BUCKET`, default `videos`) is created by SeaweedFS at startup, and
  the API also ensures it exists (idempotent), so the same code works on
  MinIO, AWS S3 or any other S3-compatible store.
- Credentials come from `S3_ACCESS_KEY` / `S3_SECRET_KEY` (no defaults in
  code; dev values only in `.env.example` and compose).

## Consequences

- Swapping the storage backend is a configuration change (`S3_ENDPOINT`,
  `S3_USE_SSL`, credentials), not a code change.
- SeaweedFS features beyond the S3 API are not used.
- `/readyz` reports `storage` as ok when the bucket exists.
