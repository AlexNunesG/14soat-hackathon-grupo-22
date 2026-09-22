# auth-service — TODO: implemented in a later plan item.
# Minimal build so `deploy/docker-compose.yml` can reference this service today.
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go build -o /out/auth-service .

FROM alpine:3.19
COPY --from=build /out/auth-service /usr/local/bin/auth-service
ENTRYPOINT ["/usr/local/bin/auth-service"]
