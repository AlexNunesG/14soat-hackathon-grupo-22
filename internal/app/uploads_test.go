package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeUploadRepo is an in-memory app.UploadRepository.
type fakeUploadRepo struct {
	videos []*domain.Video
	msgs   []app.Message
	err    error
}

func (r *fakeUploadRepo) CreateWithMessages(_ context.Context, videos []*domain.Video, msgs []app.Message) error {
	if r.err != nil {
		return r.err
	}
	r.videos, r.msgs = append(r.videos, videos...), append(r.msgs, msgs...)
	return nil
}

func newUploads(repo *fakeUploadRepo, store *fakeStorage, notified *int) *app.Uploads {
	n := 0
	return app.NewUploads(repo, store,
		app.WithUploadClock(func() time.Time { return fixedNow }),
		app.WithUploadIDs(func() string {
			n++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		}),
		app.OnEnqueued(func() { *notified++ }),
	)
}

func file(name, content string) app.UploadFile {
	return app.UploadFile{Name: name, Size: int64(len(content)), Content: strings.NewReader(content)}
}

func TestUploadStoresRecordsAndQueues(t *testing.T) {
	repo, store, notified := &fakeUploadRepo{}, newFakeStorage(), 0
	videos, err := newUploads(repo, store, &notified).Upload(context.Background(), ownerID,
		[]app.UploadFile{file("holiday.mp4", "first"), file("Trip.MKV", "second"), file("no.size.webm", "third")})
	if err != nil {
		t.Fatal(err)
	}

	wantNames := []string{"holiday.mp4", "Trip.MKV", "no.size.webm"}
	wantKeys := []string{
		"uploads/00000000-0000-4000-8000-000000000001.mp4",
		"uploads/00000000-0000-4000-8000-000000000002.mkv",
		"uploads/00000000-0000-4000-8000-000000000003.webm",
	}
	created := time.Date(2026, 9, 24, 15, 0, 0, 123456000, time.UTC) // UTC, microseconds
	if len(videos) != 3 {
		t.Fatalf("got %d videos", len(videos))
	}
	for i, v := range videos {
		if v.OriginalName != wantNames[i] || v.StorageKey != wantKeys[i] || v.OwnerID != ownerID ||
			v.Status != domain.StatusPending || !v.CreatedAt.Equal(created) || v.CreatedAt.Location() != time.UTC {
			t.Errorf("videos[%d] = %+v", i, v)
		}
	}
	if got := store.keys(); !slices.Equal(got, wantKeys) {
		t.Errorf("stored %v, want %v", got, wantKeys)
	}
	if string(store.objects[wantKeys[1]]) != "second" || store.types[wantKeys[1]] != "application/octet-stream" {
		t.Errorf("stored content %q type %q", store.objects[wantKeys[1]], store.types[wantKeys[1]])
	}
	if len(repo.videos) != 3 || len(repo.msgs) != 3 {
		t.Fatalf("repository got %d videos and %d messages", len(repo.videos), len(repo.msgs))
	}
	for i, m := range repo.msgs {
		var body map[string]string
		if err := json.Unmarshal(m.Body, &body); err != nil {
			t.Fatal(err)
		}
		if m.ID != videos[i].ID || m.Topic != app.TopicVideoUploaded || body["video_id"] != videos[i].ID || len(body) != 1 {
			t.Errorf("message %d: %+v (%s)", i, m, m.Body)
		}
	}
	if notified != 1 {
		t.Errorf("relay notified %d times, want 1", notified)
	}
}

func TestUploadIsAllOrNothing(t *testing.T) {
	for name, files := range map[string][]app.UploadFile{
		"unsupported last":  {file("a.mp4", "x"), file("notes.txt", "x")},
		"unsupported first": {file("clip", "x"), file("a.mp4", "x")},
		"double extension":  {file("clip.mp4.exe", "x")},
	} {
		t.Run(name, func(t *testing.T) {
			repo, store, notified := &fakeUploadRepo{}, newFakeStorage(), 0
			_, err := newUploads(repo, store, &notified).Upload(context.Background(), ownerID, files)
			if !errors.Is(err, domain.ErrUnsupportedFormat) {
				t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
			}
			for _, f := range domain.SupportedFormats() {
				if !strings.Contains(err.Error(), f) {
					t.Errorf("message %q does not list %s", err, f)
				}
			}
			if store.puts != 0 || len(repo.videos) != 0 || notified != 0 {
				t.Errorf("something was stored: %d puts, %d videos, %d notifications", store.puts, len(repo.videos), notified)
			}
		})
	}
}

func TestUploadWithoutFiles(t *testing.T) {
	notified := 0
	_, err := newUploads(&fakeUploadRepo{}, newFakeStorage(), &notified).Upload(context.Background(), ownerID, nil)
	if !errors.Is(err, app.ErrMissingFile) {
		t.Fatalf("err = %v, want ErrMissingFile", err)
	}
}

func TestUploadStorageFailureRemovesStoredObjects(t *testing.T) {
	repo, store, notified := &fakeUploadRepo{}, newFakeStorage(), 0
	store.putErr["uploads/00000000-0000-4000-8000-000000000002.mp4"] = errBoom
	_, err := newUploads(repo, store, &notified).Upload(context.Background(), ownerID,
		[]app.UploadFile{file("a.mp4", "1"), file("b.mp4", "2"), file("c.mp4", "3")})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the storage error", err)
	}
	if keys := store.keys(); len(keys) != 0 {
		t.Errorf("objects left behind: %v", keys)
	}
	if !slices.Equal(store.deleted, []string{"uploads/00000000-0000-4000-8000-000000000001.mp4"}) {
		t.Errorf("deleted %v", store.deleted)
	}
	if len(repo.videos) != 0 || notified != 0 {
		t.Error("videos recorded after a storage failure")
	}
}

func TestUploadDatabaseFailureRemovesStoredObjects(t *testing.T) {
	repo, store, notified := &fakeUploadRepo{err: errBoom}, newFakeStorage(), 0
	_, err := newUploads(repo, store, &notified).Upload(context.Background(), ownerID,
		[]app.UploadFile{file("a.mp4", "1"), file("b.avi", "2")})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the repository error", err)
	}
	if keys := store.keys(); len(keys) != 0 {
		t.Errorf("objects left behind: %v", keys)
	}
	if notified != 0 {
		t.Error("relay notified after a failed transaction")
	}
}

func TestVideoKey(t *testing.T) {
	for name, want := range map[string]string{
		"a.MP4":           "uploads/id.mp4",
		"my.trip.webm":    "uploads/id.webm",
		"vídeo ação.mkv":  "uploads/id.mkv",
		"no-extension":    "uploads/id",
		"trailing-dot.":   "uploads/id",
		"../../etc/x.avi": "uploads/id.avi",
	} {
		if got := app.VideoKey("id", name); got != want {
			t.Errorf("VideoKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestVideoUploadedMessageRoundTrip(t *testing.T) {
	v := &domain.Video{ID: videoID}
	m, err := app.NewVideoUploadedMessage(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.DecodeVideoUploaded(m.Body)
	if err != nil || got.VideoID != videoID {
		t.Fatalf("decoded %+v, %v", got, err)
	}
	for _, body := range []string{"", "not json", `{"video_id": ""}`, `{"other": 1}`, `[]`} {
		if _, err := app.DecodeVideoUploaded([]byte(body)); !errors.Is(err, app.ErrMalformedMessage) {
			t.Errorf("DecodeVideoUploaded(%q) = %v, want ErrMalformedMessage", body, err)
		}
	}
}
