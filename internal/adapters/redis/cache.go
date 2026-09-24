// Package rediscache is the Redis adapter of app.VideoListCache: it caches
// the pages of GET /api/v1/videos per user, invalidated by a per-user
// version (docs/cache.md). It lives in internal/adapters/redis; the package
// is named rediscache so it does not shadow the go-redis client.
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// DefaultTTL is how long a cached page lives when New gets a zero TTL.
const DefaultTTL = 30 * time.Second

// versionTTL is how long an untouched version key lives. When it expires,
// the next reader seeds a new, unique version, so every page cached under
// the old one is simply never read again.
const versionTTL = 24 * time.Hour

// Client timeouts: the cache must answer fast or be skipped, so a slow or
// unreachable Redis costs a request at most about a second.
const (
	dialTimeout = 500 * time.Millisecond
	ioTimeout   = 300 * time.Millisecond
)

// VersionKey is the key of the version of an owner's video list.
func VersionKey(ownerID string) string { return "videos:ver:" + ownerID }

// ListKey is the key of one cached page of an owner's video list at a
// version.
func ListKey(ownerID, version string, page app.Page) string {
	return "videos:list:" + ownerID + ":" + version + ":" + strconv.Itoa(page.Number) + ":" + strconv.Itoa(page.Size)
}

// getOrSeedVersion returns the version at KEYS[1], or sets it to ARGV[1]
// (with a TTL of ARGV[2] seconds) and returns it when there is none.
var getOrSeedVersion = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v then return v end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
return ARGV[1]
`)

// bumpVersion increments the version at KEYS[1], or seeds it with ARGV[1]
// when there is none, and (re)sets its TTL to ARGV[2] seconds.
var bumpVersion = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  redis.call('INCR', KEYS[1])
else
  redis.call('SET', KEYS[1], ARGV[1])
end
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1
`)

func init() {
	// go-redis prints connection errors as plain text on stderr. The cache
	// reports every failed call itself, as a structured log line of its
	// caller, so those lines would only be noisy duplicates.
	redis.SetLogger(discardLogger{})
}

type discardLogger struct{}

func (discardLogger) Printf(context.Context, string, ...any) {}

// Cache is an app.VideoListCache on Redis.
type Cache struct {
	rdb  *redis.Client
	ttl  time.Duration
	seed func() string
}

var _ app.VideoListCache = (*Cache)(nil)

// New returns a cache on the Redis at url (redis:// or rediss://). Cached
// pages live for ttl (DefaultTTL when zero). It does not connect: the first
// command does, and every command fails fast while Redis is unreachable.
func New(url string, ttl time.Duration) (*Cache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis url: %w", err)
	}
	opts.DialTimeout = dialTimeout
	opts.ReadTimeout = ioTimeout
	opts.WriteTimeout = ioTimeout
	opts.MaxRetries = 1 // one retry covers a pooled connection closed by a Redis restart
	opts.DialerRetries = 1
	opts.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{rdb: redis.NewClient(opts), ttl: ttl, seed: newSeed}, nil
}

// newSeed returns a fresh version: the current time in nanoseconds, which
// is larger than any version seeded (and bumped a few times) before.
func newSeed() string { return strconv.FormatInt(time.Now().UnixNano(), 10) }

// Close closes the connections.
func (c *Cache) Close() error { return c.rdb.Close() }

// Ping checks that Redis answers.
func (c *Cache) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// ListVersion implements app.VideoListCache.
func (c *Cache) ListVersion(ctx context.Context, ownerID string) (string, error) {
	v, err := getOrSeedVersion.Run(ctx, c.rdb, []string{VersionKey(ownerID)}, c.seed(), seconds(versionTTL)).Text()
	if err != nil {
		return "", fmt.Errorf("redis: list version: %w", err)
	}
	return v, nil
}

// GetList implements app.VideoListCache.
func (c *Cache) GetList(ctx context.Context, ownerID, version string, page app.Page) (app.VideoPage, bool, error) {
	data, err := c.rdb.Get(ctx, ListKey(ownerID, version, page)).Bytes()
	if errors.Is(err, redis.Nil) {
		return app.VideoPage{}, false, nil
	}
	if err != nil {
		return app.VideoPage{}, false, fmt.Errorf("redis: get list: %w", err)
	}
	vp, err := decodePage(data)
	if err != nil {
		return app.VideoPage{}, false, fmt.Errorf("redis: cached list: %w", err)
	}
	vp.Page = page
	return vp, true, nil
}

// PutList implements app.VideoListCache.
func (c *Cache) PutList(ctx context.Context, ownerID, version string, vp app.VideoPage) error {
	data, err := encodePage(vp)
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, ListKey(ownerID, version, vp.Page), data, c.ttl).Err(); err != nil {
		return fmt.Errorf("redis: put list: %w", err)
	}
	return nil
}

// InvalidateList implements app.ListInvalidator.
func (c *Cache) InvalidateList(ctx context.Context, ownerID string) error {
	if err := bumpVersion.Run(ctx, c.rdb, []string{VersionKey(ownerID)}, c.seed(), seconds(versionTTL)).Err(); err != nil {
		return fmt.Errorf("redis: invalidate list: %w", err)
	}
	return nil
}

func seconds(d time.Duration) int64 { return int64(d / time.Second) }

// cachedPage is the JSON of a cached page. The page number and size are in
// the key.
type cachedPage struct {
	Items []cachedVideo `json:"items"`
	Total int           `json:"total"`
}

// cachedVideo is the JSON of a domain.Video in a cached page.
type cachedVideo struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"owner_id"`
	OriginalName string    `json:"original_name"`
	StorageKey   string    `json:"storage_key"`
	ZipKey       string    `json:"zip_key,omitempty"`
	Status       string    `json:"status"`
	FrameCount   int       `json:"frame_count,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func encodePage(vp app.VideoPage) ([]byte, error) {
	p := cachedPage{Items: make([]cachedVideo, 0, len(vp.Items)), Total: vp.Total}
	for i := range vp.Items {
		v := &vp.Items[i]
		p.Items = append(p.Items, cachedVideo{
			ID: v.ID, OwnerID: v.OwnerID, OriginalName: v.OriginalName, StorageKey: v.StorageKey,
			ZipKey: v.ZipKey, Status: v.Status.String(), FrameCount: v.FrameCount,
			ErrorMessage: v.ErrorMessage, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		})
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode cached list: %w", err)
	}
	return data, nil
}

func decodePage(data []byte) (app.VideoPage, error) {
	var p cachedPage
	if err := json.Unmarshal(data, &p); err != nil {
		return app.VideoPage{}, err
	}
	vp := app.VideoPage{Items: make([]domain.Video, 0, len(p.Items)), Total: p.Total}
	for _, v := range p.Items {
		status, err := domain.ParseVideoStatus(v.Status)
		if err != nil {
			return app.VideoPage{}, err
		}
		vp.Items = append(vp.Items, domain.Video{
			ID: v.ID, OwnerID: v.OwnerID, OriginalName: v.OriginalName, StorageKey: v.StorageKey,
			ZipKey: v.ZipKey, Status: status, FrameCount: v.FrameCount,
			ErrorMessage: v.ErrorMessage, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		})
	}
	return vp, nil
}
