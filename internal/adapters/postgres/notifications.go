package postgres

import (
	"context"
	"fmt"

	"video-processor/internal/app"
)

// Notifications is the PostgreSQL app.NotificationLog: the table
// notifications_sent (db/migrations/00003_create_notifications_sent.sql).
type Notifications struct {
	db DB
}

var _ app.NotificationLog = (*Notifications)(nil)

// NewNotifications returns the notification log over db.
func NewNotifications(db DB) *Notifications { return &Notifications{db: db} }

// SendOnce inserts the event id and calls send inside one transaction,
// committed only when send succeeded:
//
//   - an event already recorded conflicts on the primary key: send is not
//     called;
//   - a concurrent SendOnce for the same event blocks on the key until the
//     first transaction ends, then skips (committed) or sends (rolled back);
//   - a failed send rolls the row back, so a retry sends again.
//
// If the process dies after the mail server accepted the e-mail but before
// the commit, the row is rolled back and a redelivery sends the e-mail
// again: the window is one round trip to the database.
func (n *Notifications) SendOnce(ctx context.Context, rec app.SentNotification, send func(ctx context.Context) error) (bool, error) {
	if !validUUID(rec.EventID) || !validUUID(rec.VideoID) {
		return false, fmt.Errorf("postgres: notification of event %q, video %q: %w: ids must be UUIDs", rec.EventID, rec.VideoID, app.ErrPermanent)
	}
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("postgres: begin notification: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO notifications_sent (event_id, video_id, kind, recipient)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		rec.EventID, rec.VideoID, rec.Kind, rec.Recipient)
	if err != nil {
		return false, fmt.Errorf("postgres: record notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already sent
	}
	if err := send(ctx); err != nil {
		return false, err
	}
	// The e-mail is out: commit even if ctx was canceled meanwhile.
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return true, fmt.Errorf("postgres: commit notification: %w: %w", app.ErrNotRecorded, err)
	}
	return true, nil
}
