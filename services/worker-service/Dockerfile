# worker-service — TODO: implemented in a later plan item.
# Minimal build so `deploy/docker-compose.yml` can reference this service today.
# NOTE: ffmpeg will need to be installed here once processing logic lands.
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go build -o /out/worker-service .

FROM alpine:3.19
COPY --from=build /out/worker-service /usr/local/bin/worker-service
ENTRYPOINT ["/usr/local/bin/worker-service"]
