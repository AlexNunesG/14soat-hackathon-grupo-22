# Observability

## Logs

Every service (api, worker, notifier) writes **one JSON object per line** to
stdout through `log/slog` (`internal/platform/logging`). `LOG_LEVEL`
(`debug`, `info`, `warn`, `error`; default `info`) sets the minimum level.

### Fields

Every line has `time`, `level`, `msg` and `service` (`api`, `worker` or
`notifier`). The **correlation fields** are added from the context of the
line (`logging.ContextHandler`), so every line about one request or one
job carries them without each call site repeating them:

| field | where | meaning |
|---|---|---|
| `request_id` | api, worker, notifier | Correlation id: the `X-Request-ID` of the HTTP request, carried by every message it caused (see below). |
| `user_id` | api | Authenticated user of the request (routes behind a bearer token). |
| `video_id` | api, worker, notifier | Video of a `/api/v1/videos/{id}` route or of the message being handled. |
| `event_id` | notifier | Id of the `video.failed` / `video.processed` event being handled. |
| `message_id` | worker, notifier | AMQP message id (video id for jobs, event id for events). |
| `attempt` | worker, notifier | Delivery attempt of the message (1 = first, see [`messaging.md`](messaging.md)). |
| `queue` | worker, notifier | Work queue of the consumer. |
| `error` | any | The error, on `warn`/`error` lines. |

Use the context-aware methods (`log.InfoContext(ctx, ...)`,
`log.ErrorContext(ctx, ...)`, `log.LogAttrs(ctx, ...)`) so the fields are
added; `logging.WithAttrs(ctx, ...)` / `logging.WithRequestID(ctx, id)`
add fields for everything downstream. An attribute passed explicitly (or
bound with `Logger.With`) wins over the context's, so no key appears twice.

**Access log (api).** One line per request, `msg: "http request"`, with
`method`, `route` (the route pattern, e.g. `/api/v1/videos/:id`, never the
raw path: bounded cardinality and no ids in it; `unmatched` for 404s of
unknown paths), `status`, `duration_ms`, `bytes` (response body size),
`request_id` and, when authenticated, `user_id` (and `video_id` on the
`{id}` routes). Level: `info`; `error` for 5xx; `debug` for the probes
(`/healthz`, `/readyz`) and the web UI's static files (`/`, `/ui/*`). The
worker's and notifier's probe servers log the same way.

### Correlation id

The api takes the correlation id from the request header `X-Request-ID`
when it is 1 to 128 characters among letters, digits and `-_.:`
(`logging.ValidRequestID`); otherwise (missing, too long, other characters)
it generates a UUID. Either way it is echoed in the response header
`X-Request-ID` ([`openapi.yaml`](openapi.yaml)) and follows the upload:

```
client ──X-Request-ID: demo-123──▶ api
  api    request ctx: request_id=demo-123, user_id                 (access log, errors)
    │    INSERT outbox (…, correlation_id='demo-123')              (migration 00004)
    │    outbox relay ──AMQP correlation-id=demo-123──▶ video.process
    ▼
  worker consumer ctx: request_id, message_id, attempt, video_id   ("processing video", …)
    │    retry republish keeps correlation-id
    │    MarkDone/MarkFailed(ctx) → outbox event row inherits correlation_id
    │    outbox relay ──video.failed, correlation-id=demo-123──▶ video.notify
    ▼
  notifier consumer ctx: request_id, message_id, attempt, event_id, video_id
         ("failure e-mail sent", retries, give-up)
```

- The outbox (`postgres.enqueue`) stores the request id of the context that
  queues a message (the upload's in the api, the job's in the worker) in
  `outbox.correlation_id`; the relay publishes it as the standard AMQP
  `correlation-id` property (`rabbitmq.Publisher`).
- The consumer (`rabbitmq.Consumer`) restores it as `request_id`, with
  `message_id`, `attempt` and the body's `video_id` / `event_id`, into the
  context passed to the handler, so every line of the job has them.
- Messages without one (rows queued before migration 00004, or sent by
  other tools) are handled normally; their lines have no `request_id` but
  still `video_id` / `message_id`.

### Following one upload

```sh
curl -H "Authorization: Bearer $TOKEN" -H "X-Request-ID: demo-123" \
     -F videos=@good.mp4 -F videos=@corrupt.mp4 http://localhost:8080/api/v1/videos
docker compose -f deploy/docker-compose.yml logs --no-color | grep demo-123
```

```
api-1      | {"level":"INFO","msg":"http request","service":"api","method":"POST","route":"/api/v1/videos","status":202,"duration_ms":10.8,"bytes":451,"request_id":"demo-123","user_id":"bf08…"}
worker-1   | {"level":"INFO","msg":"processing video","service":"worker","video_id":"1c32…","original_name":"good.mp4","request_id":"demo-123","message_id":"1c32…","attempt":1}
worker-1   | {"level":"INFO","msg":"video processed","service":"worker","video_id":"1c32…","frames":2,"duration_ms":38.1,"request_id":"demo-123",…}
worker-2   | {"level":"INFO","msg":"video cannot be processed","service":"worker","video_id":"742c…","reason":"ffmpeg: …Invalid data…","request_id":"demo-123",…}
notifier-1 | {"level":"INFO","msg":"failure e-mail sent","service":"notifier","event_id":"44c0…","video_id":"742c…","request_id":"demo-123",…}
```

Without a client-provided id, take the one the api returned
(`curl -D -` shows the `X-Request-ID` response header), or grep by
`video_id`. With `jq`:
`docker compose -f deploy/docker-compose.yml logs --no-color --no-log-prefix | grep '^{' | jq 'select(.request_id=="demo-123")'`.

### Privacy

- Never logged: passwords, bearer tokens, the `Authorization` header,
  request or e-mail bodies, the JWT secret or other credentials. The access
  log has no headers, no query string and no raw path.
- **E-mail addresses are not logged**: users are identified by `user_id`
  (`owner_id` in the worker). Where an address could end up in a logged
  error (the mailer's "invalid recipient"), it is masked with
  `logging.MaskEmail` (`ana@example.com` → `a***@example.com`). Replies
  from the SMTP server are logged as the server sent them.
- The client IP is not logged.
- A client-provided `X-Request-ID` is logged only when it passes
  `logging.ValidRequestID`, so it cannot inject quotes, newlines or huge
  values into the logs or the message properties.
