package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"video-processor/db/migrations"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// migratedPool connects to POSTGRES_TEST_URL (e.g. the compose stack's
// database) and applies the migrations, or skips the test when it is not
// set. Tests only add rows with fresh ids and e-mails, so they can share a
// database with anything else.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_URL")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := Migrate(ctx, pool, migrations.FS, nil); err != nil {
		t.Fatal(err)
	}
	return pool
}

// now is a timestamp with the database's (microsecond) precision.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func newUser(t *testing.T, users *Users) *domain.User {
	t.Helper()
	u := &domain.User{
		ID:           uuid.NewString(),
		Name:         "Test User",
		Email:        "repo-" + uuid.NewString() + "@example.com",
		PasswordHash: "hash-of-" + uuid.NewString(),
		CreatedAt:    now(),
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := migratedPool(t)
	version, err := Migrate(context.Background(), pool, migrations.FS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if version < 1 {
		t.Errorf("version %d, want >= 1", version)
	}
}

func TestUsersCreateAndGet(t *testing.T) {
	users := NewUsers(migratedPool(t))
	ctx := context.Background()
	u := newUser(t, users)

	byEmail, err := users.GetByEmail(ctx, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	if *byEmail != *u {
		t.Errorf("GetByEmail\n got %+v\nwant %+v", *byEmail, *u)
	}
	byID, err := users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *byID != *u {
		t.Errorf("GetByID\n got %+v\nwant %+v", *byID, *u)
	}
}

func TestUsersNotFound(t *testing.T) {
	users := NewUsers(migratedPool(t))
	ctx := context.Background()
	if _, err := users.GetByEmail(ctx, "nobody-"+uuid.NewString()+"@example.com"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("unknown e-mail: err = %v", err)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if _, err := users.GetByID(ctx, id); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("GetByID(%q): err = %v", id, err)
		}
	}
}

func TestUsersDuplicateEmail(t *testing.T) {
	users := NewUsers(migratedPool(t))
	u := newUser(t, users)
	dup := *u
	dup.ID = uuid.NewString()
	if err := users.Create(context.Background(), &dup); !errors.Is(err, app.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestUsersSchemaRejectsUppercaseEmail(t *testing.T) {
	// The application stores e-mails lowercased; the schema enforces it, so
	// the unique index is case-insensitive.
	users := NewUsers(migratedPool(t))
	u := &domain.User{ID: uuid.NewString(), Name: "X", Email: "Upper-" + uuid.NewString() + "@example.com",
		PasswordHash: "h", CreatedAt: now()}
	err := users.Create(context.Background(), u)
	if err == nil || errors.Is(err, app.ErrEmailTaken) {
		t.Fatalf("err = %v, want a check violation", err)
	}
}

func newVideo(t *testing.T, ownerID, name string, created time.Time) *domain.Video {
	t.Helper()
	v, err := domain.NewVideo(uuid.NewString(), ownerID, name, "videos/"+uuid.NewString(), created)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVideosCreateAndGet(t *testing.T) {
	pool := migratedPool(t)
	users, videos := NewUsers(pool), NewVideos(pool)
	ctx := context.Background()
	owner, other := newUser(t, users), newUser(t, users)

	pending := newVideo(t, owner.ID, "Holiday.MP4", now())
	done := newVideo(t, owner.ID, "done.mkv", now())
	if err := done.Start(now()); err != nil {
		t.Fatal(err)
	}
	if err := done.Complete("zips/done.zip", 12, now()); err != nil {
		t.Fatal(err)
	}
	failed := newVideo(t, owner.ID, "bad.avi", now())
	if err := failed.Fail("no video stream", now()); err != nil {
		t.Fatal(err)
	}
	for _, v := range []*domain.Video{pending, done, failed} {
		if err := videos.Create(ctx, v); err != nil {
			t.Fatalf("create %s: %v", v.OriginalName, err)
		}
		got, err := videos.GetByIDForOwner(ctx, v.ID, owner.ID)
		if err != nil {
			t.Fatalf("get %s: %v", v.OriginalName, err)
		}
		if *got != *v {
			t.Errorf("round trip\n got %+v\nwant %+v", *got, *v)
		}
	}

	// Zero values are stored as NULL.
	var zipKey, errorMessage *string
	var frameCount *int
	if err := pool.QueryRow(ctx, `SELECT zip_key, frame_count, error_message FROM videos WHERE id = $1`,
		pending.ID).Scan(&zipKey, &frameCount, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if zipKey != nil || frameCount != nil || errorMessage != nil {
		t.Errorf("PENDING video: zip_key %v, frame_count %v, error_message %v, want NULLs", zipKey, frameCount, errorMessage)
	}

	for name, tc := range map[string]struct{ id, owner string }{
		"another user's video": {pending.ID, other.ID},
		"unknown id":           {uuid.NewString(), owner.ID},
		"malformed id":         {"not-a-uuid", owner.ID},
		"malformed owner":      {pending.ID, "nope"},
	} {
		if _, err := videos.GetByIDForOwner(ctx, tc.id, tc.owner); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestVideosSchemaEnforcesInvariants(t *testing.T) {
	pool := migratedPool(t)
	users, videos := NewUsers(pool), NewVideos(pool)
	owner := newUser(t, users)
	for name, edit := range map[string]func(*domain.Video){
		"unknown status":         func(v *domain.Video) { v.Status = "UPLOADED" },
		"DONE without zip":       func(v *domain.Video) { v.Status = domain.StatusDone },
		"FAILED without message": func(v *domain.Video) { v.Status = domain.StatusFailed },
		"message when PENDING":   func(v *domain.Video) { v.ErrorMessage = "oops" },
		"unknown owner":          func(v *domain.Video) { v.OwnerID = uuid.NewString() },
	} {
		v := newVideo(t, owner.ID, "x.mp4", now())
		edit(v)
		if err := videos.Create(context.Background(), v); err == nil {
			t.Errorf("%s: insert accepted", name)
		}
	}
}

func TestVideosListByOwner(t *testing.T) {
	pool := migratedPool(t)
	users, videos := NewUsers(pool), NewVideos(pool)
	ctx := context.Background()
	owner, other := newUser(t, users), newUser(t, users)

	// Two videos share a timestamp (as in one multi-file upload): the id
	// breaks the tie, descending.
	base := now()
	var created []*domain.Video
	for i, at := range []time.Time{base, base.Add(time.Second), base.Add(2 * time.Second), base.Add(2 * time.Second)} {
		v := newVideo(t, owner.ID, "v"+string(rune('a'+i))+".mp4", at)
		if err := videos.Create(ctx, v); err != nil {
			t.Fatal(err)
		}
		created = append(created, v)
	}
	if err := videos.Create(ctx, newVideo(t, other.ID, "other.mp4", base.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	newest := []*domain.Video{created[2], created[3]}
	if newest[0].ID < newest[1].ID {
		newest[0], newest[1] = newest[1], newest[0]
	}
	want := []string{newest[0].ID, newest[1].ID, created[1].ID, created[0].ID}

	tests := []struct {
		page    app.Page
		wantIDs []string
	}{
		{app.Page{Number: 1, Size: 20}, want},
		{app.Page{Number: 1, Size: 3}, want[:3]},
		{app.Page{Number: 2, Size: 3}, want[3:]},
		{app.Page{Number: 3, Size: 3}, nil},
		{app.Page{Number: 1 << 40, Size: 100}, nil},
	}
	for _, tt := range tests {
		items, total, err := videos.ListByOwner(ctx, owner.ID, tt.page)
		if err != nil {
			t.Fatalf("%+v: %v", tt.page, err)
		}
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		if total != 4 || strings.Join(ids, ",") != strings.Join(tt.wantIDs, ",") {
			t.Errorf("%+v: total %d ids %v, want 4 and %v", tt.page, total, ids, tt.wantIDs)
		}
		if items == nil {
			t.Errorf("%+v: items is nil, want empty", tt.page)
		}
	}

	items, total, err := videos.ListByOwner(ctx, owner.ID, app.Page{Number: 1, Size: 1})
	if err != nil || len(items) != 1 || items[0] != *videoByID(created, want[0]) || total != 4 {
		t.Errorf("full item: %+v total %d err %v", items, total, err)
	}

	nobody, total, err := videos.ListByOwner(ctx, uuid.NewString(), app.Page{Number: 1, Size: 20})
	if err != nil || len(nobody) != 0 || total != 0 {
		t.Errorf("user without videos: %d items, total %d, err %v", len(nobody), total, err)
	}
}

// videoByID returns the created video with the id.
func videoByID(created []*domain.Video, id string) *domain.Video {
	for _, v := range created {
		if v.ID == id {
			return v
		}
	}
	return nil
}
