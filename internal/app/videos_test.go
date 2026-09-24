package app_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeVideos records the calls of an app.VideoRepository.
type fakeVideos struct {
	items     []domain.Video
	total     int
	err       error
	gotOwner  string
	gotID     string
	gotPage   app.Page
	listCalls int
	getCalls  int
}

func (*fakeVideos) Create(context.Context, *domain.Video) error { return nil }

func (f *fakeVideos) GetByIDForOwner(_ context.Context, id, ownerID string) (*domain.Video, error) {
	f.getCalls++
	f.gotID, f.gotOwner = id, ownerID
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.items {
		if f.items[i].ID == id && f.items[i].OwnerID == ownerID {
			return &f.items[i], nil
		}
	}
	return nil, app.ErrNotFound
}

func (f *fakeVideos) ListByOwner(_ context.Context, ownerID string, page app.Page) ([]domain.Video, int, error) {
	f.listCalls++
	f.gotOwner, f.gotPage = ownerID, page
	return f.items, f.total, f.err
}

const (
	ownerID = "11111111-1111-4111-8111-111111111111"
	videoID = "22222222-2222-4222-8222-222222222222"
)

func TestPageValidate(t *testing.T) {
	tests := []struct {
		page  app.Page
		field string // "" when valid
	}{
		{app.Page{Number: 1, Size: 1}, ""},
		{app.Page{Number: 1000, Size: 100}, ""},
		{app.Page{Number: 0, Size: 20}, "page"},
		{app.Page{Number: -1, Size: 20}, "page"},
		{app.Page{Number: 1, Size: 0}, "page_size"},
		{app.Page{Number: 1, Size: 101}, "page_size"},
		{app.Page{Number: 1, Size: -1}, "page_size"},
	}
	for _, tt := range tests {
		err := tt.page.Validate()
		var verr *app.ValidationError
		switch {
		case tt.field == "" && err != nil:
			t.Errorf("%+v: unexpected error %v", tt.page, err)
		case tt.field != "" && (!errors.As(err, &verr) || verr.Field != tt.field):
			t.Errorf("%+v: err = %v, want a ValidationError on %s", tt.page, err, tt.field)
		}
	}
}

func TestPageOffset(t *testing.T) {
	tests := []struct {
		page app.Page
		want int64
	}{
		{app.Page{Number: 1, Size: 20}, 0},
		{app.Page{Number: 2, Size: 20}, 20},
		{app.Page{Number: 100, Size: 2}, 198},
		{app.Page{Number: math.MaxInt, Size: 100}, math.MaxInt64}, // saturates
	}
	for _, tt := range tests {
		if got := tt.page.Offset(); got != tt.want {
			t.Errorf("%+v.Offset() = %d, want %d", tt.page, got, tt.want)
		}
	}
}

func TestListVideos(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID}}, total: 7}
	page, err := app.NewVideos(repo, newFakeStorage()).List(context.Background(), ownerID, app.Page{Number: 2, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if repo.gotOwner != ownerID || repo.gotPage != (app.Page{Number: 2, Size: 5}) {
		t.Errorf("repository called with owner %q page %+v", repo.gotOwner, repo.gotPage)
	}
	if len(page.Items) != 1 || page.Total != 7 || page.Page != (app.Page{Number: 2, Size: 5}) {
		t.Errorf("page %+v", page)
	}
}

func TestListVideosEmptyIsNotNil(t *testing.T) {
	page, err := app.NewVideos(&fakeVideos{}, newFakeStorage()).List(context.Background(), ownerID, app.Page{Number: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil {
		t.Error("Items is nil, want an empty slice")
	}
}

func TestListVideosRejectsInvalidPage(t *testing.T) {
	repo := &fakeVideos{}
	_, err := app.NewVideos(repo, newFakeStorage()).List(context.Background(), ownerID, app.Page{Number: 1, Size: 101})
	if !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if repo.listCalls != 0 {
		t.Error("the repository was queried for an invalid page")
	}
}

func TestListVideosRepositoryError(t *testing.T) {
	repo := &fakeVideos{err: errors.New("timeout")}
	if _, err := app.NewVideos(repo, newFakeStorage()).List(context.Background(), ownerID, app.Page{Number: 1, Size: 20}); err == nil {
		t.Fatal("want error")
	}
}

func TestGetVideo(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, OriginalName: "a.mp4"}}}
	v, err := app.NewVideos(repo, newFakeStorage()).Get(context.Background(), ownerID, videoID)
	if err != nil {
		t.Fatal(err)
	}
	if v.OriginalName != "a.mp4" || repo.gotOwner != ownerID || repo.gotID != videoID {
		t.Errorf("video %+v, repository called with id %q owner %q", v, repo.gotID, repo.gotOwner)
	}
}

func TestGetVideoNotFound(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID}}}
	videos := app.NewVideos(repo, newFakeStorage())
	other := "33333333-3333-4333-8333-333333333333"
	for name, tc := range map[string]struct{ owner, id string }{
		"unknown id":           {ownerID, other},
		"another user's video": {other, videoID},
	} {
		if _, err := videos.Get(context.Background(), tc.owner, tc.id); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}

	calls := repo.getCalls
	for _, id := range []string{"not-a-uuid", "", "22222222222242228222222222222222", "{" + videoID + "}"} {
		if _, err := videos.Get(context.Background(), ownerID, id); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("malformed id %q: err = %v, want ErrNotFound", id, err)
		}
	}
	if repo.getCalls != calls {
		t.Error("the repository was queried for a malformed id")
	}
}

func TestDownload(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{
		ID: videoID, OwnerID: ownerID, OriginalName: "a.mp4", Status: domain.StatusDone, ZipKey: "frames/z.zip", FrameCount: 2,
	}}}
	store := newFakeStorage()
	store.objects["frames/z.zip"] = []byte("zip!")
	v, obj, err := app.NewVideos(repo, store).Download(context.Background(), ownerID, videoID)
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if v.ID != videoID || obj.Size != 4 {
		t.Errorf("video %+v, size %d", v, obj.Size)
	}
}

func TestDownloadNotReady(t *testing.T) {
	for _, status := range []domain.VideoStatus{domain.StatusPending, domain.StatusProcessing, domain.StatusFailed} {
		repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, Status: status}}}
		_, _, err := app.NewVideos(repo, newFakeStorage()).Download(context.Background(), ownerID, videoID)
		var notReady *app.NotReadyError
		if !errors.As(err, &notReady) || !errors.Is(err, app.ErrVideoNotReady) || notReady.Status != status {
			t.Fatalf("%s: err = %v, want a NotReadyError", status, err)
		}
		if want := "video is not ready for download (status: " + string(status) + ")"; err.Error() != want {
			t.Errorf("message %q, want %q", err, want)
		}
	}
}

func TestDownloadNotFoundAndMissingArchive(t *testing.T) {
	repo := &fakeVideos{items: []domain.Video{{ID: videoID, OwnerID: ownerID, Status: domain.StatusDone, ZipKey: "gone.zip"}}}
	videos := app.NewVideos(repo, newFakeStorage())
	other := "33333333-3333-4333-8333-333333333333"
	if _, _, err := videos.Download(context.Background(), other, videoID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another user's video: err = %v, want ErrNotFound", err)
	}
	if _, _, err := videos.Download(context.Background(), ownerID, "nope"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("malformed id: err = %v, want ErrNotFound", err)
	}
	// A DONE video whose archive is gone is a server error, not a 404.
	if _, _, err := videos.Download(context.Background(), ownerID, videoID); err == nil || errors.Is(err, app.ErrNotFound) {
		t.Errorf("missing archive: err = %v, want a non-NotFound error", err)
	}
}
