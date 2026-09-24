package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// Videos is the PostgreSQL video repository: app.VideoRepository,
// app.UploadRepository and app.ProcessingRepository. Zero FrameCount, ZipKey
// and ErrorMessage are stored as NULL.
type Videos struct {
	db DB
}

var (
	_ app.VideoRepository      = (*Videos)(nil)
	_ app.UploadRepository     = (*Videos)(nil)
	_ app.ProcessingRepository = (*Videos)(nil)
)

// NewVideos returns the video repository over db.
func NewVideos(db DB) *Videos { return &Videos{db: db} }

const videoColumns = `id, user_id, original_name, storage_key, zip_key, status,
	frame_count, error_message, created_at, updated_at`

// Create inserts v.
func (r *Videos) Create(ctx context.Context, v *domain.Video) error {
	return insertVideo(ctx, r.db, v)
}

func insertVideo(ctx context.Context, db Querier, v *domain.Video) error {
	_, err := db.Exec(ctx,
		`INSERT INTO videos (`+videoColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		v.ID, v.OwnerID, v.OriginalName, v.StorageKey, nullString(v.ZipKey), string(v.Status),
		nullInt(v.FrameCount), nullString(v.ErrorMessage), v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: insert video: %w", err)
	}
	return nil
}

// CreateWithMessages inserts the videos and queues the messages in the
// outbox in one transaction (ADR 0004).
func (r *Videos) CreateWithMessages(ctx context.Context, videos []*domain.Video, msgs []app.Message) error {
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		for _, v := range videos {
			if err := insertVideo(ctx, tx, v); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, msgs)
	})
}

// GetByID returns the video with the id, whoever owns it. Unknown and
// malformed ids yield an error wrapping app.ErrNotFound.
func (r *Videos) GetByID(ctx context.Context, id string) (*domain.Video, error) {
	if !validUUID(id) {
		return nil, fmt.Errorf("postgres: video %q: %w", id, app.ErrNotFound)
	}
	v, err := scanVideo(r.db.QueryRow(ctx, `SELECT `+videoColumns+` FROM videos WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("postgres: video %q: %w", id, app.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: select video: %w", err)
	}
	return v, nil
}

// MarkProcessing moves a PENDING or PROCESSING video to PROCESSING and
// reports whether it did.
func (r *Videos) MarkProcessing(ctx context.Context, id string, at time.Time) (bool, error) {
	return r.update(ctx, "mark processing", `
		UPDATE videos SET status = 'PROCESSING', updated_at = $2
		WHERE id = $1 AND status IN ('PENDING', 'PROCESSING')`, id, at)
}

// MarkDone moves a PROCESSING video to DONE and reports whether it did;
// only then it queues events in the outbox, in the same transaction.
func (r *Videos) MarkDone(ctx context.Context, id, zipKey string, frameCount int, at time.Time, events ...app.Message) (bool, error) {
	return r.updateWithEvents(ctx, "mark done", events, `
		UPDATE videos SET status = 'DONE', zip_key = $2, frame_count = $3, updated_at = $4
		WHERE id = $1 AND status = 'PROCESSING'`, id, zipKey, frameCount, at)
}

// MarkFailed moves a PENDING or PROCESSING video to FAILED and reports
// whether it did; only then it queues events in the outbox, in the same
// transaction.
func (r *Videos) MarkFailed(ctx context.Context, id, reason string, at time.Time, events ...app.Message) (bool, error) {
	return r.updateWithEvents(ctx, "mark failed", events, `
		UPDATE videos SET status = 'FAILED', error_message = $2, updated_at = $3
		WHERE id = $1 AND status IN ('PENDING', 'PROCESSING')`, id, reason, at)
}

// update runs a conditional UPDATE of one video and reports whether it
// changed a row.
func (r *Videos) update(ctx context.Context, op, sql, id string, args ...any) (bool, error) {
	if !validUUID(id) {
		return false, nil
	}
	tag, err := r.db.Exec(ctx, sql, append([]any{id}, args...)...)
	if err != nil {
		return false, fmt.Errorf("postgres: %s: %w", op, err)
	}
	return tag.RowsAffected() == 1, nil
}

// updateWithEvents is update plus, when the row changed, the events queued
// in the outbox, all in one transaction (ADR 0004): the run whose UPDATE
// wins is the only one that records the events.
func (r *Videos) updateWithEvents(ctx context.Context, op string, events []app.Message, sql, id string, args ...any) (bool, error) {
	if len(events) == 0 {
		return r.update(ctx, op, sql, id, args...)
	}
	if !validUUID(id) {
		return false, nil
	}
	changed := false
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, append([]any{id}, args...)...)
		if err != nil {
			return fmt.Errorf("postgres: %s: %w", op, err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		changed = true
		return enqueue(ctx, tx, events)
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// GetByIDForOwner returns the owner's video with the id. Unknown and
// malformed ids and other users' videos yield an error wrapping
// app.ErrNotFound.
func (r *Videos) GetByIDForOwner(ctx context.Context, id, ownerID string) (*domain.Video, error) {
	if !validUUID(id) || !validUUID(ownerID) {
		return nil, fmt.Errorf("postgres: video %q: %w", id, app.ErrNotFound)
	}
	row := r.db.QueryRow(ctx,
		`SELECT `+videoColumns+` FROM videos WHERE id = $1 AND user_id = $2`, id, ownerID)
	v, err := scanVideo(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("postgres: video %q: %w", id, app.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: select video: %w", err)
	}
	return v, nil
}

// ListByOwner returns one page of the owner's videos, newest first
// (created_at, then id, descending), and the owner's total. The page and
// the total come from one statement, so they are consistent.
func (r *Videos) ListByOwner(ctx context.Context, ownerID string, page app.Page) ([]domain.Video, int, error) {
	if !validUUID(ownerID) {
		return nil, 0, nil
	}
	// The LEFT JOIN keeps one row holding the total when the page is empty.
	rows, err := r.db.Query(ctx, `
		WITH total AS (SELECT count(*) AS n FROM videos WHERE user_id = $1)
		SELECT total.n, v.id, v.user_id, v.original_name, v.storage_key, v.zip_key, v.status,
		       v.frame_count, v.error_message, v.created_at, v.updated_at
		FROM total
		LEFT JOIN LATERAL (
			SELECT `+videoColumns+` FROM videos
			WHERE user_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2 OFFSET $3
		) v ON true`,
		ownerID, page.Size, page.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list videos: %w", err)
	}
	defer rows.Close()

	var (
		total  int
		videos = []domain.Video{}
	)
	for rows.Next() {
		var (
			id, owner, originalName, storageKey, status *string
			zipKey, errorMessage                        *string
			frameCount                                  *int
			createdAt, updatedAt                        *time.Time
		)
		if err := rows.Scan(&total, &id, &owner, &originalName, &storageKey, &zipKey, &status,
			&frameCount, &errorMessage, &createdAt, &updatedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres: scan video: %w", err)
		}
		if id == nil { // empty page: only the total
			continue
		}
		videos = append(videos, domain.Video{
			ID:           *id,
			OwnerID:      *owner,
			OriginalName: *originalName,
			StorageKey:   *storageKey,
			ZipKey:       deref(zipKey),
			Status:       domain.VideoStatus(*status),
			FrameCount:   deref(frameCount),
			ErrorMessage: deref(errorMessage),
			CreatedAt:    createdAt.UTC(),
			UpdatedAt:    updatedAt.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: list videos: %w", err)
	}
	return videos, total, nil
}

// scanVideo reads one row of videoColumns.
func scanVideo(row pgx.Row) (*domain.Video, error) {
	var (
		v                    domain.Video
		status               string
		zipKey, errorMessage *string
		frameCount           *int
	)
	if err := row.Scan(&v.ID, &v.OwnerID, &v.OriginalName, &v.StorageKey, &zipKey, &status,
		&frameCount, &errorMessage, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.Status = domain.VideoStatus(status)
	v.ZipKey, v.FrameCount, v.ErrorMessage = deref(zipKey), deref(frameCount), deref(errorMessage)
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return &v, nil
}

// validUUID reports whether s is a canonical UUID. Other strings cannot
// match a uuid column; checking first avoids a database error for them.
func validUUID(s string) bool {
	return len(s) == 36 && uuid.Validate(s) == nil
}
