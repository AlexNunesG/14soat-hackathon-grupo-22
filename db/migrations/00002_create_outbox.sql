-- Transactional outbox (PLAN.md Phase 2.3, ADR 0004, deliverable D2).
--
-- The api inserts a video and the message announcing it in the same
-- transaction; a relay in the api publishes the messages to RabbitMQ (with
-- publisher confirms) and deletes each row once the broker has confirmed it.
-- So an upload answered 202 always gets its job queued, even if the broker
-- was down at upload time (RF2).

-- +goose Up
CREATE TABLE outbox (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- publish order
    message_id   text        NOT NULL CHECK (message_id <> ''),        -- AMQP message-id
    topic        text        NOT NULL CHECK (topic <> ''),             -- routing key
    payload      jsonb       NOT NULL,                                 -- message body
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Failed publish attempts so far, and the last error, for operators.
    attempts     integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   text,
    -- The relay skips a row until then: failed publishes back off.
    available_at timestamptz NOT NULL DEFAULT now()
);

-- The relay claims the oldest available rows:
--   ... WHERE available_at <= now() ORDER BY id LIMIT n FOR UPDATE SKIP LOCKED
CREATE INDEX outbox_available_idx ON outbox (available_at, id);

-- +goose Down
DROP TABLE outbox;
