package app

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/google/uuid"

	"video-processor/internal/domain"
)

// Pagination limits of GET /api/v1/videos (docs/openapi.yaml).
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Page selects one page of a listing: Number is 1-based.
type Page struct {
	Number int
	Size   int
}

// Offset is the number of items before the page. It saturates instead of
// overflowing for absurdly large page numbers (such a page is just empty).
func (p Page) Offset() int64 {
	if p.Number <= 1 || p.Size <= 0 {
		return 0
	}
	if int64(p.Number-1) > math.MaxInt64/int64(p.Size) {
		return math.MaxInt64
	}
	return int64(p.Number-1) * int64(p.Size)
}

// Validate checks the page against the contract's limits.
func (p Page) Validate() error {
	if p.Number < 1 {
		return invalid("page", "must be an integer >= 1")
	}
	if p.Size < 1 || p.Size > MaxPageSize {
		return invalid("page_size", "must be an integer between 1 and %d", MaxPageSize)
	}
	return nil
}

// VideoPage is one page of a user's videos.
type VideoPage struct {
	Items []domain.Video
	Page  Page
	Total int
}

// Videos holds the read use cases of a user's videos (RF4): list, get and
// download. Every call is scoped to the calling user (RF3).
type Videos struct {
	repo    VideoRepository
	storage ObjectStorage
	cache   VideoListCache
	log     *slog.Logger
}

// VideosOption customizes Videos.
type VideosOption func(*Videos)

// WithListCache serves List from cache when it can (see VideoListCache).
// Cache errors are logged to log and the repository answers instead.
func WithListCache(cache VideoListCache, log *slog.Logger) VideosOption {
	return func(v *Videos) {
		v.cache = cache
		if log != nil {
			v.log = log
		}
	}
}

// NewVideos returns the video use cases.
func NewVideos(repo VideoRepository, storage ObjectStorage, opts ...VideosOption) *Videos {
	v := &Videos{repo: repo, storage: storage, log: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// List returns one page of the owner's videos, newest first. An invalid
// page yields a *ValidationError.
func (v *Videos) List(ctx context.Context, ownerID string, page Page) (VideoPage, error) {
	if err := page.Validate(); err != nil {
		return VideoPage{}, err
	}
	if v.cache == nil {
		return v.list(ctx, ownerID, page)
	}

	// The version is read before the repository, so a page that races with
	// a change is stored under the version that change made stale.
	version, err := v.cache.ListVersion(ctx, ownerID)
	if err != nil {
		v.cacheError(ctx, "read the video list version", ownerID, err)
		return v.list(ctx, ownerID, page)
	}
	cached, hit, err := v.cache.GetList(ctx, ownerID, version, page)
	switch {
	case err != nil:
		v.cacheError(ctx, "read a cached video list", ownerID, err)
	case hit:
		v.log.DebugContext(ctx, "video list served from cache", slog.String("owner_id", ownerID),
			slog.Int("page", page.Number), slog.Int("page_size", page.Size))
		if cached.Items == nil {
			cached.Items = []domain.Video{}
		}
		return cached, nil
	}

	vp, err := v.list(ctx, ownerID, page)
	if err != nil {
		return VideoPage{}, err
	}
	if err := v.cache.PutList(ctx, ownerID, version, vp); err != nil {
		v.cacheError(ctx, "cache a video list", ownerID, err)
	}
	return vp, nil
}

// list reads one page from the repository.
func (v *Videos) list(ctx context.Context, ownerID string, page Page) (VideoPage, error) {
	items, total, err := v.repo.ListByOwner(ctx, ownerID, page)
	if err != nil {
		return VideoPage{}, fmt.Errorf("list videos: %w", err)
	}
	if items == nil {
		items = []domain.Video{}
	}
	return VideoPage{Items: items, Page: page, Total: total}, nil
}

// cacheError logs a failed cache operation; the caller carries on without
// the cache.
func (v *Videos) cacheError(ctx context.Context, what, ownerID string, err error) {
	v.log.WarnContext(ctx, "could not "+what+"; using the database",
		slog.String("owner_id", ownerID), slog.Any("error", err))
}

// Get returns one of the owner's videos. An unknown or malformed id and
// another user's video all yield an error wrapping ErrNotFound, so the
// existence of other users' videos is not leaked.
func (v *Videos) Get(ctx context.Context, ownerID, id string) (*domain.Video, error) {
	if !isCanonicalUUID(id) {
		return nil, fmt.Errorf("video %q: %w", id, ErrNotFound)
	}
	video, err := v.repo.GetByIDForOwner(ctx, id, ownerID)
	if err != nil {
		return nil, fmt.Errorf("get video: %w", err)
	}
	return video, nil
}

// isCanonicalUUID reports whether s is a UUID in its canonical
// 8-4-4-4-12 form, the only one the API hands out.
func isCanonicalUUID(s string) bool {
	return len(s) == 36 && uuid.Validate(s) == nil
}

// Download opens the frames archive of one of the owner's videos; the
// caller closes it. Unknown, malformed and other users' ids yield an error
// wrapping ErrNotFound, and a video that is not DONE a *NotReadyError.
func (v *Videos) Download(ctx context.Context, ownerID, id string) (*domain.Video, *Object, error) {
	video, err := v.Get(ctx, ownerID, id)
	if err != nil {
		return nil, nil, err
	}
	if video.Status != domain.StatusDone {
		return nil, nil, &NotReadyError{Status: video.Status}
	}
	obj, err := v.storage.Get(ctx, video.ZipKey)
	if err != nil {
		return nil, nil, fmt.Errorf("open frames archive of video %s: %w", id, err)
	}
	return video, obj, nil
}
