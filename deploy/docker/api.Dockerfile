# syntax=docker/dockerfile:1
# Image of the api service. Build from the repository root:
#   docker build -f deploy/docker/api.Dockerfile .
# (deploy/docker-compose.yml does this for the local stack.)

FROM golang:1.27-alpine AS build
WORKDIR /src
# Optional module proxy override, e.g. a local mirror; empty means Go's
# default (https://proxy.golang.org,direct).
ARG GOPROXY
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22
# busybox wget (used by the compose healthcheck) and CA certificates come
# with the base image.
RUN adduser -D -H -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
