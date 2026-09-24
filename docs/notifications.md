# Failure notifications (RF5)

When a video ends `FAILED`, its owner receives one e-mail at the address
used to register. The subject contains the original file name and the body
contains the video's `error_message` (`docs/openapi.yaml`, "Failure
notification (RF5)"). A video that ends `DONE` sends no e-mail.

```
worker ──(one transaction: videos.status = FAILED + outbox row video.failed)──> PostgreSQL
worker outbox relay ──video.failed (confirmed, mandatory)──> [videos] ──> video.notify
notifier ──consume──> notifications_sent (dedup) ──SMTP──> MailHog / mail server
```

## Events

The worker records a `video.failed` event when a video ends `FAILED`, both
when the file cannot be processed and when the retries run out (GiveUp). It
records a `video.processed` event when a video ends `DONE`. The event is
inserted into the outbox **in the same transaction as the status change**
([ADR 0004](adr/0004-transactional-outbox.md)), and only if the conditional
`UPDATE` changed the row. So:

- an event exists exactly when its status change was committed: a crash
  cannot leave a `FAILED` video without its event, or an event for a video
  that is not `FAILED`;
- when several deliveries of a job race, only the run that wins the status
  change writes an event.

The worker's outbox relay (same code as the api's) publishes the events
with publisher confirms. While RabbitMQ is down they wait in the outbox,
and while the notifier is down they wait in `video.notify`. The payload is
self-contained (event id, video id, owner id, e-mail and name, original
name, upload time, error message or frame count, time of the event). The
owner's e-mail is read when the event is recorded. Schema:
[`messaging.md`](messaging.md#video-events).

`video.processed` has no consumer yet. It is published without the
mandatory flag, so while no queue is bound the broker drops it
([`messaging.md`](messaging.md#messages)).

## The notifier service

`cmd/notifier` consumes `video.notify` (`video.failed` only) and sends one
plain-text e-mail per event through SMTP:

- **To**: the owner's registered e-mail. **From**: `SMTP_FROM`.
- **Subject**: `Video processing failed: <original name>`, as RFC 2047
  UTF-8 encoded-words when the name is not plain ASCII (e.g.
  `=?utf-8?q?Video_processing_failed:_f=C3=A9rias.mp4?=`).
- **Body** (`text/plain; charset=utf-8`, quoted-printable): the file name,
  the upload time (UTC), `Reason: <error_message>` verbatim, a link to the
  web UI (`APP_URL`) and the video id.
- **Message-ID**: `<event id@sender domain>`, so a receiving server can
  also spot a resend.

It serves `GET /healthz` (liveness, used by the compose healthcheck) and
`GET /readyz` (database and broker) on `HEALTH_ADDR`. The mail server is
not a readiness check: while it is down, events wait in the retry queues.
On SIGTERM it stops consuming, lets in-flight e-mails finish for up to
`SHUTDOWN_TIMEOUT` and requeues the rest.

### Retries and dead-lettering

| Failure | What happens |
|---|---|
| Connection refused / timeout, TLS error, AUTH refused, 4xx reply, database down | Retried after 10 s, 1 min, 5 min and 15 min (`video.notify.retry.N`); after the 5th attempt (`NOTIFIER_MAX_ATTEMPTS`), dead-lettered to `video.notify.dlq` |
| 5xx reply to MAIL FROM, RCPT TO or DATA (e.g. `550 no such user`), invalid recipient address | Dead-lettered at once, no retry (`app.ErrPermanent`) |
| Malformed event | Dead-lettered at once |

A rejected login (535) is treated as transient: it is a configuration
problem that affects every e-mail, so the events are retried while the
operator fixes it rather than all dead-lettered. Events in the DLQ can be
replayed with the management UI (shovel or "move messages" back to
`video.notify`). Deduplication makes a replay safe.

### Exactly one e-mail

Delivery is at least once: the outbox relay can publish an event twice (a
crash after the confirm, before deleting the row), and the broker
redelivers unacknowledged messages. The notifier deduplicates by **event
id** with the table `notifications_sent` (event id as primary key,
migration `00003`):

1. begin a transaction and `INSERT ... ON CONFLICT (event_id) DO NOTHING`;
   no row inserted means the e-mail was already sent, so ack and skip;
2. send the e-mail;
3. commit if the send succeeded, roll back if it failed (the retry sends
   again).

A concurrent duplicate (two notifier replicas holding copies of the same
event) blocks on the primary key until the first transaction ends, then
skips (committed) or sends (rolled back). So two copies are never sent at
the same time.

**Known window**: if the notifier dies, or the commit fails, *after* the
SMTP server accepted the e-mail but *before* the commit, the row is not
recorded and a redelivery sends the e-mail again. The window is one
database round trip. Closing it would need the mail server to take part in
the transaction, which SMTP cannot do. If the commit fails in a running
notifier, the event is acked anyway (logged as "sent, but not recorded"),
because retrying would certainly send a second e-mail. In normal operation,
including notifier and broker restarts, each failed video sends exactly one
e-mail.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `SMTP_HOST` | `localhost` (compose: `mailhog`) | SMTP server |
| `SMTP_PORT` | `1025` (MailHog) | Port: usually 587 with `starttls`, 465 with `tls` |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | empty | PLAIN authentication when the username is set. Requires `starttls` or `tls`: credentials are never sent in clear text |
| `SMTP_FROM` | `FIAP X Video Processor <no-reply@fiapx.local>` | Sender (RFC 5322 address) |
| `SMTP_TLS` | `none` | `none` (plain SMTP, MailHog), `starttls` (upgrade required; fails if the server does not offer it) or `tls` (implicit TLS). Certificates are verified against the system CAs |
| `SMTP_TIMEOUT` | `30s` | Bound on sending one e-mail |
| `APP_URL` | `http://localhost:8080` | Link to the web UI in the e-mail |
| `NOTIFIER_CONCURRENCY` | `4` | E-mails sent at once (prefetch) |
| `NOTIFIER_MAX_ATTEMPTS` | `0` (= 5) | Attempts before the DLQ |
| `HEALTH_ADDR`, `SHUTDOWN_TIMEOUT`, `READINESS_TIMEOUT`, `LOG_LEVEL`, `DATABASE_URL`, `AMQP_URL` | as the worker | |

Production example (any provider's SMTP relay):

```sh
SMTP_HOST=smtp.example.com SMTP_PORT=587 SMTP_TLS=starttls \
SMTP_USERNAME=apikey SMTP_PASSWORD=<from the secret store> \
SMTP_FROM='FIAP X <no-reply@example.com>' APP_URL=https://videos.example.com
```

## Operating it

- Locally, e-mails are caught by MailHog: <http://localhost:8025>.
- E-mails sent: `SELECT * FROM notifications_sent ORDER BY sent_at DESC;`
- Events not published yet: `SELECT topic, count(*) FROM outbox GROUP BY topic;`
- E-mails waiting for a retry or given up on: queues
  `video.notify.retry.*` and `video.notify.dlq` in the management UI
  (<http://localhost:15672>).

## Tests

- Unit: `internal/app/notifier_test.go` (fake mailer and log: one e-mail
  per event, retries, permanent failures, "sent but not recorded"),
  `internal/app/events_test.go` (events recorded only by the winning
  change), `internal/adapters/mailer/smtp_test.go` (a scripted SMTP server:
  MIME encoding, 4xx/5xx classification, STARTTLS required, implicit TLS
  with AUTH, timeouts).
- Against the stack (`make up`): `SMTP_TEST_ADDR=localhost:1025
  MAILHOG_TEST_URL=http://localhost:8025 go test ./internal/adapters/mailer/`
  and `POSTGRES_TEST_URL=... go test ./internal/adapters/postgres/`
  (outbox events, `SendOnce` with concurrent duplicates).
- Black box: `tests/integration/notification_test.go` and `e2e_test.go`.
