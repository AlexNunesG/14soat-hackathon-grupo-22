package rediscache

import (
	"context"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

var testPage = app.VideoPage{
	Items: []domain.Video{
		{
			ID: "22222222-2222-4222-8222-222222222222", OwnerID: "owner", OriginalName: "holiday.mp4",
			StorageKey: "uploads/a.mp4", ZipKey: "frames/a/r.zip", Status: domain.StatusDone, FrameCount: 12,
			CreatedAt: time.Date(2026, 9, 24, 12, 0, 0, 123456000, time.UTC),
			UpdatedAt: time.Date(2026, 9, 24, 12, 0, 9, 0, time.UTC),
		},
		{
			ID: "33333333-3333-4333-8333-333333333333", OwnerID: "owner", OriginalName: "broken.avi",
			StorageKey: "uploads/b.avi", Status: domain.StatusFailed, ErrorMessage: "no video stream",
			CreatedAt: time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 9, 24, 11, 0, 1, 0, time.UTC),
		},
	},
	Page:  app.Page{Number: 2, Size: 2},
	Total: 7,
}

func TestKeys(t *testing.T) {
	if got := VersionKey("u1"); got != "videos:ver:u1" {
		t.Errorf("VersionKey = %q", got)
	}
	if got := ListKey("u1", "42", app.Page{Number: 3, Size: 20}); got != "videos:list:u1:42:3:20" {
		t.Errorf("ListKey = %q", got)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	data, err := encodePage(testPage)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePage(data)
	if err != nil {
		t.Fatal(err)
	}
	want := testPage
	want.Page = app.Page{} // the page is in the key, not the value
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}

	empty, err := encodePage(app.VideoPage{Items: nil})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodePage(empty); err != nil || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("empty page: %+v, %v (want a non-nil empty slice)", got, err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, data := range []string{"{", `{"items":[{"status":"LOST"}]}`} {
		if _, err := decodePage([]byte(data)); err == nil {
			t.Errorf("decodePage(%s): want error", data)
		}
	}
}

func TestNewRejectsInvalidURL(t *testing.T) {
	for _, u := range []string{"", "http://localhost:6379", "redis://host:notaport"} {
		if _, err := New(u, 0); err == nil {
			t.Errorf("New(%q): want error", u)
		}
	}
}

func TestUnreachableRedisFailsFast(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c, err := New("redis://"+addr+"/0", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	start := time.Now()
	if _, err := c.ListVersion(ctx, "owner"); err == nil {
		t.Error("ListVersion: want error")
	}
	if _, _, err := c.GetList(ctx, "owner", "1", app.Page{Number: 1, Size: 20}); err == nil {
		t.Error("GetList: want error")
	}
	if err := c.PutList(ctx, "owner", "1", testPage); err == nil {
		t.Error("PutList: want error")
	}
	if err := c.InvalidateList(ctx, "owner"); err == nil {
		t.Error("InvalidateList: want error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("four failing calls took %s", d)
	}
}

// TestAgainstRealRedis runs the cache against a real Redis when
// REDIS_TEST_URL is set, e.g. the compose stack's:
//
//	REDIS_TEST_URL=redis://localhost:6379/15 go test ./internal/adapters/redis/
//
// It uses owner ids unique to the run, so it does not touch other keys.
func TestAgainstRealRedis(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := New(url, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("test-owner-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		keys, _ := c.rdb.Keys(context.Background(), "videos:*:"+owner+"*").Result()
		if len(keys) > 0 {
			c.rdb.Del(context.Background(), keys...)
		}
	})
	page := testPage.Page

	// The first read seeds the version; later reads return the same one.
	v1, err := c.ListVersion(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.ListVersion(ctx, owner); err != nil || again != v1 {
		t.Fatalf("second ListVersion = %q, %v; want %q", again, err, v1)
	}
	if ttl := c.rdb.TTL(ctx, VersionKey(owner)).Val(); ttl <= 0 || ttl > versionTTL {
		t.Errorf("version TTL %s", ttl)
	}

	// Miss, put, hit.
	if _, hit, err := c.GetList(ctx, owner, v1, page); err != nil || hit {
		t.Fatalf("empty cache: hit %v, err %v", hit, err)
	}
	if err := c.PutList(ctx, owner, v1, testPage); err != nil {
		t.Fatal(err)
	}
	got, hit, err := c.GetList(ctx, owner, v1, page)
	if err != nil || !hit || !reflect.DeepEqual(got, testPage) {
		t.Fatalf("hit %v err %v page\n got %+v\nwant %+v", hit, err, got, testPage)
	}
	if ttl := c.rdb.TTL(ctx, ListKey(owner, v1, page)).Val(); ttl <= 0 || ttl > 2*time.Second {
		t.Errorf("list entry TTL %s, want <= 2s", ttl)
	}
	if _, hit, _ := c.GetList(ctx, owner, v1, app.Page{Number: 1, Size: 2}); hit {
		t.Error("another page hit the cache")
	}
	if _, hit, _ := c.GetList(ctx, "other-"+owner, v1, page); hit {
		t.Error("another owner hit the cache")
	}

	// Invalidation bumps the version: the old entry is not read any more.
	if err := c.InvalidateList(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v2, err := c.ListVersion(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	n1, _ := strconv.ParseInt(v1, 10, 64)
	if n2, _ := strconv.ParseInt(v2, 10, 64); n2 != n1+1 {
		t.Errorf("version after invalidation %s, want %d", v2, n1+1)
	}
	if _, hit, _ := c.GetList(ctx, owner, v2, page); hit {
		t.Error("the new version hit the old entry")
	}

	// The entry expires.
	time.Sleep(2100 * time.Millisecond)
	if _, hit, _ := c.GetList(ctx, owner, v1, page); hit {
		t.Error("entry still cached after its TTL")
	}

	// Invalidating an owner without a version seeds a fresh, unique one.
	fresh := "fresh-" + owner
	if err := c.InvalidateList(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	v, err := c.ListVersion(ctx, fresh)
	if err != nil || v == "" || v == "1" {
		t.Errorf("seeded version %q, %v", v, err)
	}
	c.rdb.Del(context.Background(), VersionKey(fresh))
}
