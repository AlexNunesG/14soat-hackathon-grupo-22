# syntax=docker/dockerfile:1
# Image of the worker service. Build from the repository root:
#   docker build -f deploy/docker/worker.Dockerfile .
# (deploy/docker-compose.yml does this for the local stack.)

FROM golang:1.27-alpine AS build
WORKDIR /src
# Optional module proxy override, e.g. a local mirror; empty means Go's
# default (https://proxy.golang.org,direct).
ARG GOPROXY
# Version reported by the videoproc_build_info metric.
ARG VERSION=dev
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY db/ db/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X video-processor/internal/platform/metrics.Version=${VERSION}" -o /out/worker ./cmd/worker

FROM alpine:3.22
# ffmpeg is a static build (pinned by tag): no package download at build
# time, and the same binary everywhere. busybox wget (used by the compose
# healthcheck) comes with the base image.
COPY --from=mwader/static-ffmpeg:7.1 /ffmpeg /usr/local/bin/ffmpeg
RUN adduser -D -H -u 10001 app
COPY --from=build /out/worker /usr/local/bin/worker
USER 10001:10001
# GET /healthz, /readyz and /metrics (HEALTH_ADDR, internal only).
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/worker"]
