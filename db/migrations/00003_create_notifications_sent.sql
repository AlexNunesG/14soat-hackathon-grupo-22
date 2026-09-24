-- Notifications already sent (PLAN.md Phase 2.5, RF5, deliverable D2).
--
-- The notifier receives each video.failed event at least once (outbox
-- republish, broker redelivery). Before sending the e-mail of an event it
-- inserts the event id here, in a transaction that it commits only after
-- the mail server accepted the e-mail; a duplicate of the event then hits
-- the primary key and is skipped. A concurrent duplicate waits on the key
-- until the first transaction ends (see docs/notifications.md).

-- +goose Up
CREATE TABLE notifications_sent (
    event_id  uuid        PRIMARY KEY,                      -- VideoEvent.event_id
    video_id  uuid        NOT NULL,                         -- no FK: history outlives videos
    kind      text        NOT NULL CHECK (kind <> ''),      -- event type, e.g. video.failed
    recipient text        NOT NULL CHECK (recipient <> ''), -- e-mail address it was sent to
    sent_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notifications_sent_video_idx ON notifications_sent (video_id);

-- +goose Down
DROP TABLE notifications_sent;
