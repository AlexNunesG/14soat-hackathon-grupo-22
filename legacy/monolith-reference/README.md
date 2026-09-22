# Monolith Reference (Legacy)

This directory holds the **original prototype** handed out for the FIAP X hackathon: a single Gin
HTTP server (`main.go`) that synchronously accepts a video upload, shells out to `ffmpeg` to
extract one frame per second, zips the resulting frames, and returns the archive in the same HTTP
response.

It has no authentication, no persistence, no message queue, no per-user concept, and its
`Dockerfile` was explicitly labeled by the original authors as not following best practices.

## Status

This code is **kept for historical reference only** and is **no longer part of the active build**.
It is not wired into `go.work`, the root `.gitignore`, `deploy/docker-compose.yml`, or any CI
workflow. Do not extend it — new functionality belongs in the microservices under `/services`.

See [`.ai-agents/PLAN.md`](../../.ai-agents/PLAN.md) for the roadmap that replaces this prototype
and [`docs/architecture.md`](../../docs/architecture.md) for the target architecture.

## Running it (if you need to compare behavior)

```bash
cd legacy/monolith-reference
go run main.go
```

It listens on the port configured in `main.go` and expects `ffmpeg` to be available on `PATH`.
