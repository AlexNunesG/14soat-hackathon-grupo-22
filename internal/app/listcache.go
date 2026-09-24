package app

import (
	"context"
	"log/slog"
	"time"
)

// invalidateTimeout bounds a best-effort list invalidation.
const invalidateTimeout = 2 * time.Second

// invalidateList bumps the version of the owner's cached list, best effort:
// an error is only logged, since the change it follows is already
// committed. Cached pages live for a short TTL, which bounds how long a
// failed invalidation can leave them stale. It runs even when ctx is
// canceled (e.g. the client went away after the commit).
func invalidateList(ctx context.Context, inv ListInvalidator, log *slog.Logger, ownerID string) {
	if inv == nil || ownerID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidateTimeout)
	defer cancel()
	if err := inv.InvalidateList(ctx, ownerID); err != nil {
		log.WarnContext(ctx, "could not invalidate the cached video list",
			slog.String("owner_id", ownerID), slog.Any("error", err))
	}
}
