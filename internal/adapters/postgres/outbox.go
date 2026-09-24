package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"video-processor/internal/app"
	"video-processor/internal/platform/logging"
)

// Backoff of messages whose publish failed: 1s, 2s, 4s, ... up to
// MaxRelayBackoff between attempts.
const (
	baseRelayBackoff = time.Second
	MaxRelayBackoff  = 15 * time.Second
	// maxLastError bounds the error text kept on an outbox row.
	maxLastError = 1000
)

// Outbox is the PostgreSQL app.OutboxStore: the table outbox
// (db/migrations/00002_create_outbox.sql and 00004, ADR 0004).
type Outbox struct {
	db DB
}

var _ app.OutboxStore = (*Outbox)(nil)

// NewOutbox returns the outbox store over db.
func NewOutbox(db DB) *Outbox { return &Outbox{db: db} }

// enqueue inserts msgs into the outbox, within the caller's transaction. A
// message without a CorrelationID gets the request id of ctx (the upload's
// X-Request-ID in the api, the job's in the worker; logging.RequestID).
func enqueue(ctx context.Context, tx pgx.Tx, msgs []app.Message) error {
	for _, m := range msgs {
		corr := m.CorrelationID
		if corr == "" {
			corr = logging.RequestID(ctx)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox (message_id, topic, payload, correlation_id) VALUES ($1, $2, $3, NULLIF($4, ''))`,
			m.ID, m.Topic, string(m.Body), corr); err != nil {
			return fmt.Errorf("postgres: insert outbox message: %w", err)
		}
	}
	return nil
}

// Relay claims up to limit due messages, oldest first, with
// FOR UPDATE SKIP LOCKED (rows claimed by another relay are skipped, not
// waited for), publishes them, deletes the published ones and delays the
// others with exponential backoff, all in one transaction. If the
// transaction does not commit, every claimed row stays in the outbox and
// is published again later: delivery is at least once.
func (o *Outbox) Relay(ctx context.Context, limit int, publish app.PublishFunc) (int, error) {
	claimed := 0
	err := pgx.BeginFunc(ctx, o.db, func(tx pgx.Tx) error {
		ids, msgs, err := claim(ctx, tx, limit)
		if err != nil || len(msgs) == 0 {
			return err
		}
		claimed = len(msgs)

		errs := publish(ctx, msgs)
		if len(errs) != len(msgs) {
			return fmt.Errorf("postgres: outbox: publish returned %d results for %d messages", len(errs), len(msgs))
		}
		var sent []int64
		for i, perr := range errs {
			if perr == nil {
				sent = append(sent, ids[i])
				continue
			}
			if err := delay(ctx, tx, ids[i], perr); err != nil {
				return err
			}
		}
		if len(sent) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id = ANY($1)`, sent); err != nil {
				return fmt.Errorf("postgres: delete published outbox messages: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return claimed, fmt.Errorf("postgres: outbox relay: %w", err)
	}
	return claimed, nil
}

func claim(ctx context.Context, tx pgx.Tx, limit int) ([]int64, []app.Message, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, message_id, topic, payload::text, COALESCE(correlation_id, '') FROM outbox
		WHERE available_at <= clock_timestamp()
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: claim outbox messages: %w", err)
	}
	defer rows.Close()
	var (
		ids  []int64
		msgs []app.Message
	)
	for rows.Next() {
		var (
			id   int64
			m    app.Message
			body string
		)
		if err := rows.Scan(&id, &m.ID, &m.Topic, &body, &m.CorrelationID); err != nil {
			return nil, nil, fmt.Errorf("postgres: scan outbox message: %w", err)
		}
		m.Body = []byte(body)
		ids, msgs = append(ids, id), append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres: claim outbox messages: %w", err)
	}
	return ids, msgs, nil
}

// delay records a failed publish of row id and postpones its next attempt.
func delay(ctx context.Context, tx pgx.Tx, id int64, cause error) error {
	msg := cause.Error()
	if len(msg) > maxLastError {
		msg = msg[:maxLastError]
	}
	_, err := tx.Exec(ctx, `
		UPDATE outbox SET
			attempts = attempts + 1,
			last_error = $2,
			available_at = clock_timestamp() + interval '1 second' * LEAST(
				$3::float8 * power(2, LEAST(attempts, 16)),
				$4::float8)
		WHERE id = $1`, id, msg, baseRelayBackoff.Seconds(), MaxRelayBackoff.Seconds())
	if err != nil {
		return fmt.Errorf("postgres: delay outbox message: %w", err)
	}
	return nil
}

// Pending returns the number of messages waiting in the outbox: not
// published yet, whether due now or delayed after a failed publish (the
// videoproc_outbox_pending metric).
func (o *Outbox) Pending(ctx context.Context) (int64, error) {
	var n int64
	if err := o.db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count outbox messages: %w", err)
	}
	return n, nil
}
