-- Correlation id of outbox messages (PLAN.md Phase 3, docs/observability.md).
--
-- The request id of the upload that caused a message (X-Request-ID) is
-- stored with it and published as the AMQP correlation-id property, so the
-- logs of the api, the worker and the notifier about one upload share it.
-- Nullable: rows queued before this migration, or outside any request,
-- have none and are published without it.

-- +goose Up
ALTER TABLE outbox ADD COLUMN correlation_id text CHECK (correlation_id <> '');

-- +goose Down
ALTER TABLE outbox DROP COLUMN correlation_id;
