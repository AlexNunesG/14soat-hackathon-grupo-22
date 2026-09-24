package integration

// Integration tests for GET /api/v1/videos (listing, pagination) and
// GET /api/v1/videos/{id} (docs/openapi.yaml, operationIds listVideos and
// getVideo).

import (
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestListShowsOnlyTheCallersVideos(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, aliceToken := registerAndLogin(t)
	_, bobToken := registerAndLogin(t)
	data := makeMP4(t, 1)
	alice := mustUpload(t, aliceToken, namedFile{"alice-1.mp4", data}, namedFile{"alice-2.mp4", data})
	bob := mustUpload(t, bobToken, namedFile{"bob.mp4", data})

	for _, tc := range []struct {
		name  string
		token string
		want  []video
	}{
		{"alice", aliceToken, alice},
		{"bob", bobToken, bob},
	} {
		page := listVideos(t, tc.token, "")
		if page.Total != len(tc.want) {
			t.Errorf("%s: total = %d, want %d", tc.name, page.Total, len(tc.want))
		}
		if got, want := sortedCopy(videoIDs(page.Items)), sortedCopy(videoIDs(tc.want)); !slices.Equal(got, want) {
			t.Errorf("%s: listed ids %v, want exactly their own %v", tc.name, got, want)
		}
	}
}

func TestListIsNewestFirst(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	data := makeMP4(t, 1)
	// Separate requests more than a second apart, so created_at strictly
	// increases even if the server stores it with one-second resolution.
	var uploaded []video
	for i, name := range []string{"first.mp4", "second.mp4", "third.mp4"} {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		uploaded = append(uploaded, uploadOne(t, token, name, data))
	}

	page := listVideos(t, token, "")

	want := []string{uploaded[2].ID, uploaded[1].ID, uploaded[0].ID}
	if got := videoIDs(page.Items); !slices.Equal(got, want) {
		t.Errorf("list order = %v, want newest first %v", got, want)
	}
	assertNewestFirst(t, page.Items)
}

// assertNewestFirst checks the contract's order: created_at descending, id
// descending as tiebreak.
func assertNewestFirst(t *testing.T, items []video) {
	t.Helper()
	for i := 1; i < len(items); i++ {
		prev, _ := time.Parse(time.RFC3339, items[i-1].CreatedAt)
		cur, _ := time.Parse(time.RFC3339, items[i].CreatedAt)
		if prev.Before(cur) || (prev.Equal(cur) && items[i-1].ID < items[i].ID) {
			t.Errorf("items[%d] (%s, %s) is listed before the newer items[%d] (%s, %s)",
				i-1, items[i-1].CreatedAt, items[i-1].ID, i, items[i].CreatedAt, items[i].ID)
		}
	}
}

func TestListPagination(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	data := makeMP4(t, 1)
	mustUpload(t, token, namedFile{"a.mp4", data}, namedFile{"b.mp4", data}, namedFile{"c.mp4", data})

	all := listVideos(t, token, "")
	if all.Page != 1 || all.PageSize != 20 || all.Total != 3 || len(all.Items) != 3 {
		t.Fatalf("default page: got page %d, page_size %d, total %d, %d items; want 1, 20, 3, 3",
			all.Page, all.PageSize, all.Total, len(all.Items))
	}

	cases := []struct {
		query     string
		page      int
		wantItems []video
	}{
		{"page_size=2", 1, all.Items[:2]},
		{"page=1&page_size=2", 1, all.Items[:2]},
		{"page=2&page_size=2", 2, all.Items[2:]},
		{"page=3&page_size=2", 3, nil},
		{"page=100&page_size=2", 100, nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := listVideos(t, token, tc.query)
			if got.Page != tc.page || got.PageSize != 2 || got.Total != 3 {
				t.Errorf("page %d, page_size %d, total %d; want %d, 2, 3", got.Page, got.PageSize, got.Total, tc.page)
			}
			if ids, want := videoIDs(got.Items), videoIDs(tc.wantItems); !slices.Equal(ids, want) {
				t.Errorf("items = %v, want %v", ids, want)
			}
		})
	}
}

func TestListInvalidPaginationIsRejected(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	for _, query := range []string{
		"page=0",
		"page=-1",
		"page=abc",
		"page=1.5",
		"page_size=0",
		"page_size=101",
		"page_size=-1",
		"page_size=abc",
	} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			resp, body := authGet(t, token, "/api/v1/videos?"+query)
			assertError(t, resp, body, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestGetVideoMatchesListItem(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	uploaded := uploadOne(t, token, "holiday.mp4", makeMP4(t, 1))
	// Compare once the video is final, so nothing changes between the calls.
	waitForStatus(t, token, uploaded.ID, statusDone)

	page := listVideos(t, token, "")
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 listed video, got %d", len(page.Items))
	}
	got := getVideo(t, token, uploaded.ID)
	if !reflect.DeepEqual(got, page.Items[0]) {
		t.Errorf("GET by id and the list disagree:\n  get:  %s\n  list: %s", describe(got), describe(page.Items[0]))
	}
}

func TestGetVideoNotFound(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, ownerToken := registerAndLogin(t)
	_, otherToken := registerAndLogin(t)
	owned := uploadOne(t, ownerToken, "private.mp4", makeMP4(t, 1))

	for name, id := range map[string]string{
		"unknown id":           randomUUID(t),
		"malformed id":         "not-a-uuid",
		"another user's video": owned.ID,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, body := authGet(t, otherToken, "/api/v1/videos/"+id)
			assertError(t, resp, body, http.StatusNotFound, "not_found")
		})
	}
}

func TestDownloadURLOnlyWhenDone(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	// decodeVideo (used by every helper that reads a video) fails when
	// download_url is present on a video that is not DONE, and when it is
	// present with another value than /api/v1/videos/{id}/download. The
	// contract does not require it on DONE videos, so its absence there is
	// accepted.
	videos := mustUpload(t, token, namedFile{"good.mp4", makeMP4(t, 1)}, namedFile{"bad.mp4", corruptVideo()})
	for _, v := range videos {
		if v.DownloadURL != nil {
			t.Errorf("%s: download_url present on a PENDING video", v.OriginalName)
		}
	}

	done := waitForStatus(t, token, videos[0].ID, statusDone)
	failed := waitForStatus(t, token, videos[1].ID, statusFailed)

	if failed.DownloadURL != nil {
		t.Errorf("download_url present on a FAILED video: %q", *failed.DownloadURL)
	}
	// Both final states read the same way from the list.
	for _, item := range listVideos(t, token, "").Items {
		switch item.ID {
		case done.ID:
			if !reflect.DeepEqual(item, done) {
				t.Errorf("DONE video differs in the list: %s vs %s", describe(item), describe(done))
			}
		case failed.ID:
			if item.DownloadURL != nil {
				t.Errorf("download_url present on a FAILED video in the list: %q", *item.DownloadURL)
			}
		}
	}
}
