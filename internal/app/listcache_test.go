package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeListCache is an in-memory app.VideoListCache with the same version
// scheme as the Redis one.
type fakeListCache struct {
	mu       sync.Mutex
	versions map[string]int
	pages    map[string]app.VideoPage
	err      map[string]error // by operation: version, get, put, invalidate
	bumps    map[string]int   // invalidations by owner
	puts     int
	hits     int
	// afterVersion runs after ListVersion returns (to simulate a change
	// racing with a read).
	afterVersion func()
	// onInvalidate runs at the start of InvalidateList.
	onInvalidate func(owner string)
}

func newFakeListCache() *fakeListCache {
	return &fakeListCache{
		versions: map[string]int{}, pages: map[string]app.VideoPage{},
		err: map[string]error{}, bumps: map[string]int{},
	}
}

func pageKey(owner, version string, p app.Page) string {
	return fmt.Sprintf("%s:%s:%d:%d", owner, version, p.Number, p.Size)
}

func (c *fakeListCache) ListVersion(_ context.Context, owner string) (string, error) {
	c.mu.Lock()
	err, v := c.err["version"], c.versions[owner]
	hook := c.afterVersion
	c.mu.Unlock()
	if err != nil {
		return "", err
	}
	if hook != nil {
		hook()
	}
	return fmt.Sprint(v), nil
}

func (c *fakeListCache) GetList(_ context.Context, owner, version string, p app.Page) (app.VideoPage, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.err["get"]; err != nil {
		return app.VideoPage{}, false, err
	}
	vp, ok := c.pages[pageKey(owner, version, p)]
	if ok {
		c.hits++
	}
	return vp, ok, nil
}

func (c *fakeListCache) PutList(_ context.Context, owner, version string, vp app.VideoPage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	if err := c.err["put"]; err != nil {
		return err
	}
	c.pages[pageKey(owner, version, vp.Page)] = vp
	return nil
}

func (c *fakeListCache) InvalidateList(_ context.Context, owner string) error {
	if c.onInvalidate != nil {
		c.onInvalidate(owner)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.err["invalidate"]; err != nil {
		return err
	}
	c.versions[owner]++
	c.bumps[owner]++
	return nil
}

// ownerBumps returns how many times ownerID's list was invalidated.
func (c *fakeListCache) ownerBumps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bumps[ownerID]
}

var firstPage = app.Page{Number: 1, Size: 20}

func TestListCacheMissThenHit(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusPending}}, total: 1}
	cache := newFakeListCache()
	videos := app.NewVideos(repo, newFakeStorage(), app.WithListCache(cache, nil))

	first, err := videos.List(context.Background(), ownerID, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	second, err := videos.List(context.Background(), ownerID, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	if repo.listCalls != 1 || cache.puts != 1 || cache.hits != 1 {
		t.Errorf("repository calls %d, puts %d, hits %d; want 1, 1, 1", repo.listCalls, cache.puts, cache.hits)
	}
	if len(second.Items) != 1 || second.Total != 1 || second.Page != firstPage || second.Items[0].ID != first.Items[0].ID {
		t.Errorf("cached page %+v", second)
	}

	// Pages are cached separately.
	if _, err := videos.List(context.Background(), ownerID, app.Page{Number: 2, Size: 20}); err != nil {
		t.Fatal(err)
	}
	if repo.listCalls != 2 {
		t.Errorf("another page was served from the first one's cache entry")
	}
}

func TestListCacheInvalidationServesFreshPage(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusPending}}, total: 1}
	cache := newFakeListCache()
	videos := app.NewVideos(repo, newFakeStorage(), app.WithListCache(cache, nil))
	if _, err := videos.List(context.Background(), ownerID, firstPage); err != nil {
		t.Fatal(err)
	}

	repo.items = []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusDone}}
	if err := cache.InvalidateList(context.Background(), ownerID); err != nil {
		t.Fatal(err)
	}
	page, err := videos.List(context.Background(), ownerID, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Status != domain.StatusDone || repo.listCalls != 2 {
		t.Errorf("status %s after invalidation, repository calls %d", page.Items[0].Status, repo.listCalls)
	}
}

func TestListCacheIsPerOwner(t *testing.T) {
	repo := &fakeVideos{}
	cache := newFakeListCache()
	videos := app.NewVideos(repo, newFakeStorage(), app.WithListCache(cache, nil))
	other := "33333333-3333-4333-8333-333333333333"
	for _, owner := range []string{ownerID, other} {
		if _, err := videos.List(context.Background(), owner, firstPage); err != nil {
			t.Fatal(err)
		}
		if repo.gotOwner != owner {
			t.Fatalf("repository queried for %q, want %q", repo.gotOwner, owner)
		}
	}
	if repo.listCalls != 2 {
		t.Errorf("repository calls %d, want 2 (one per owner)", repo.listCalls)
	}
}

// A change committed while a reader queries the repository must not leave
// the old page cached under the new version.
func TestListCacheChangeRacingWithReadIsNotServedStale(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusPending}}, total: 1}
	cache := newFakeListCache()
	videos := app.NewVideos(repo, newFakeStorage(), app.WithListCache(cache, nil))

	// The reader gets the version, then the change lands (and bumps it)
	// before the reader's page is stored: that page is PENDING.
	cache.afterVersion = func() {
		cache.afterVersion = nil
		_ = cache.InvalidateList(context.Background(), ownerID)
	}
	page, err := videos.List(context.Background(), ownerID, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Status != domain.StatusPending {
		t.Fatalf("status %s", page.Items[0].Status)
	}
	repo.items = []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusProcessing}}

	page, err = videos.List(context.Background(), ownerID, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Status != domain.StatusProcessing {
		t.Errorf("stale page served: status %s", page.Items[0].Status)
	}
}

func TestListCacheErrorsFallBackToRepository(t *testing.T) {
	for _, op := range []string{"version", "get", "put"} {
		t.Run(op, func(t *testing.T) {
			repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID}}, total: 1}
			cache := newFakeListCache()
			cache.err[op] = errors.New("redis: connection refused")
			var logs bytes.Buffer
			videos := app.NewVideos(repo, newFakeStorage(),
				app.WithListCache(cache, slog.New(slog.NewTextHandler(&logs, nil))))

			for range 2 {
				page, err := videos.List(context.Background(), ownerID, firstPage)
				if err != nil {
					t.Fatalf("err = %v, want the repository's page", err)
				}
				if len(page.Items) != 1 || page.Total != 1 {
					t.Errorf("page %+v", page)
				}
			}
			if repo.listCalls != 2 {
				t.Errorf("repository calls %d, want 2", repo.listCalls)
			}
			if !strings.Contains(logs.String(), "connection refused") {
				t.Errorf("cache error not logged: %s", logs.String())
			}
		})
	}
}

func TestListCacheRepositoryErrorIsNotCached(t *testing.T) {
	repo := &fakeVideos{err: errors.New("db down")}
	cache := newFakeListCache()
	videos := app.NewVideos(repo, newFakeStorage(), app.WithListCache(cache, nil))
	if _, err := videos.List(context.Background(), ownerID, firstPage); err == nil {
		t.Fatal("want error")
	}
	if cache.puts != 0 {
		t.Error("a failed listing was cached")
	}
}

func TestListCacheInvalidPageSkipsCache(t *testing.T) {
	cache := newFakeListCache()
	cache.err["version"] = errors.New("must not be called")
	_, err := app.NewVideos(&fakeVideos{}, newFakeStorage(), app.WithListCache(cache, nil)).
		List(context.Background(), ownerID, app.Page{Number: 0, Size: 20})
	if !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestUploadInvalidatesListAfterCommit(t *testing.T) {
	repo, cache := &fakeUploadRepo{}, newFakeListCache()
	cache.onInvalidate = func(string) {
		if len(repo.videos) != 2 {
			t.Error("list invalidated before the videos were committed")
		}
	}
	uploads := app.NewUploads(repo, newFakeStorage(), app.WithUploadListInvalidator(cache))
	if _, err := uploads.Upload(context.Background(), ownerID,
		[]app.UploadFile{file("a.mp4", "x"), file("b.mp4", "y")}); err != nil {
		t.Fatal(err)
	}
	if got := cache.ownerBumps(); got != 1 {
		t.Errorf("%d invalidations, want 1", got)
	}
}

func TestUploadFailureDoesNotInvalidate(t *testing.T) {
	cache := newFakeListCache()
	uploads := app.NewUploads(&fakeUploadRepo{err: errBoom}, newFakeStorage(), app.WithUploadListInvalidator(cache))
	if _, err := uploads.Upload(context.Background(), ownerID, []app.UploadFile{file("a.mp4", "x")}); err == nil {
		t.Fatal("want error")
	}
	if got := cache.ownerBumps(); got != 0 {
		t.Errorf("%d invalidations, want 0", got)
	}
}

func TestUploadSucceedsWhenInvalidationFails(t *testing.T) {
	repo, cache := &fakeUploadRepo{}, newFakeListCache()
	cache.err["invalidate"] = errors.New("redis: connection refused")
	var logs bytes.Buffer
	uploads := app.NewUploads(repo, newFakeStorage(), app.WithUploadListInvalidator(cache),
		app.WithUploadLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	videos, err := uploads.Upload(context.Background(), ownerID, []app.UploadFile{file("a.mp4", "x")})
	if err != nil || len(videos) != 1 || len(repo.videos) != 1 {
		t.Fatalf("videos %v, err %v", videos, err)
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("invalidation error not logged: %s", logs.String())
	}
}

func (e *processorEnv) cachedProcessor(cache *fakeListCache, log *slog.Logger) *app.Processor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return app.NewProcessor(e.repo, e.users, e.store, e.extractor, e.archiver,
		app.WithProcessorTempDir(e.tempDir), app.WithProcessorLogger(log),
		app.WithProcessorIDs(func() string { return "run-1" }),
		app.WithProcessorListInvalidator(cache))
}

func TestProcessInvalidatesListOnEveryStatusChange(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	cache := newFakeListCache()
	var seen []domain.VideoStatus
	cache.onInvalidate = func(owner string) {
		if owner != ownerID {
			t.Errorf("invalidated owner %q", owner)
		}
		seen = append(seen, env.repo.video(videoID).Status) // already committed
	}
	if err := env.cachedProcessor(cache, nil).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[PROCESSING DONE]" {
		t.Errorf("invalidated after %v, want [PROCESSING DONE]", seen)
	}
}

func TestProcessInvalidatesListOnFailure(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.extractor.err = fmt.Errorf("no video stream: %w", app.ErrUnprocessableVideo)
	cache := newFakeListCache()
	var seen []domain.VideoStatus
	cache.onInvalidate = func(string) { seen = append(seen, env.repo.video(videoID).Status) }
	if err := env.cachedProcessor(cache, nil).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[PROCESSING FAILED]" {
		t.Errorf("invalidated after %v, want [PROCESSING FAILED]", seen)
	}
}

func TestGiveUpInvalidatesList(t *testing.T) {
	v := pendingVideo()
	v.Status = domain.StatusProcessing
	env := newProcessorEnv(t, v)
	cache := newFakeListCache()
	p := env.cachedProcessor(cache, nil)
	if err := p.GiveUp(context.Background(), jobBody(t, videoID), errBoom); err != nil {
		t.Fatal(err)
	}
	if got := cache.ownerBumps(); got != 1 {
		t.Errorf("%d invalidations, want 1", got)
	}
	// Nothing changes for a final video: no invalidation.
	if err := p.GiveUp(context.Background(), jobBody(t, videoID), errBoom); err != nil {
		t.Fatal(err)
	}
	if got := cache.ownerBumps(); got != 1 {
		t.Errorf("%d invalidations, want still 1", got)
	}
}

func TestProcessIgnoredJobDoesNotInvalidate(t *testing.T) {
	done := pendingVideo()
	done.Status, done.ZipKey, done.FrameCount = domain.StatusDone, "old.zip", 1
	env := newProcessorEnv(t, done)
	cache := newFakeListCache()
	if err := env.cachedProcessor(cache, nil).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if got := cache.ownerBumps(); got != 0 {
		t.Errorf("%d invalidations, want 0", got)
	}
}

func TestProcessSucceedsWhenInvalidationFails(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	cache := newFakeListCache()
	cache.err["invalidate"] = errors.New("redis: connection refused")
	var logs bytes.Buffer
	p := env.cachedProcessor(cache, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := p.Process(context.Background(), videoID); err != nil {
		t.Fatalf("err = %v, want the job to succeed without the cache", err)
	}
	if got := env.repo.video(videoID).Status; got != domain.StatusDone {
		t.Errorf("status %s, want DONE", got)
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("invalidation error not logged: %s", logs.String())
	}
}
