package integration

// Integration tests for RF1, "process more than one video at the same time"
// (docs/openapi.yaml, "Processing semantics": videos are processed
// asynchronously by one or more workers).
//
// Deployment assumption: the stack under test has at least 2 concurrent
// processing slots (worker replicas × per-worker concurrency). The compose
// file must provide them; with a single slot RF1 is not met and
// TestUploadsAreProcessedInParallel fails.

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// slowVideoSeconds is the length of the videos that must be observed while
// PROCESSING: see makeSlowVideo.
const slowVideoSeconds = 120

// parallelPollInterval is how often TestUploadsAreProcessedInParallel samples
// the statuses. Each slow video stays PROCESSING for a second or more, so it
// shows up in several samples.
const parallelPollInterval = 150 * time.Millisecond

// TestUploadsAreProcessedInParallel uploads N slow videos at once and samples
// the owner's list, one request per sample, until all are final. RF1 holds
// when some sample shows at least 2 of them PROCESSING at the same time.
//
// Not parallel with other tests on purpose: it runs while no other test has
// videos in flight, so the stack's processing slots are free for these N
// videos and the check does not depend on other tests' load.
func TestUploadsAreProcessedInParallel(t *testing.T) {
	notImplemented(t)
	const n = 4
	_, token := registerAndLogin(t)
	data := makeSlowVideo(t, slowVideoSeconds)
	files := make([]namedFile, n)
	for i := range files {
		files[i] = namedFile{fmt.Sprintf("parallel-%d.mp4", i+1), data}
	}
	ids := videoIDs(mustUpload(t, token, files...))

	maxProcessing, samples := 0, 0
	final := waitForFinalStatuses(t, token, ids, parallelPollInterval, processingTimeout(t), func(snapshot map[string]video) {
		samples++
		processing := 0
		for _, id := range ids {
			if snapshot[id].Status == statusProcessing {
				processing++
			}
		}
		maxProcessing = max(maxProcessing, processing)
	})

	if maxProcessing < 2 {
		t.Errorf("RF1: at most %d of %d videos were PROCESSING at the same time in %d samples taken every %s; want at least 2 (the stack needs >= 2 processing slots)",
			maxProcessing, n, samples, parallelPollInterval)
	}
	for _, id := range ids {
		v := final[id]
		if v.Status != statusDone {
			t.Errorf("video %s ended %s, want DONE: %s", v.OriginalName, v.Status, describe(v))
			continue
		}
		if *v.FrameCount != slowVideoSeconds {
			t.Errorf("video %s: frame_count = %d, want %d", v.OriginalName, *v.FrameCount, slowVideoSeconds)
		}
		_, zipData := downloadZip(t, token, id)
		assertFrames(t, zipData, slowVideoSeconds)
	}
}

// TestUploadsFromSeveralUsersAtOnceAllComplete has two users upload at the
// same instant; both users' videos are processed and each user gets the
// frames of their own videos.
func TestUploadsFromSeveralUsersAtOnceAllComplete(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	type upload struct {
		token string
		files []namedFile
		resp  *http.Response
		body  []byte
		err   error
	}
	uploads := []*upload{
		{files: []namedFile{{"alice-2s.mp4", makeMP4(t, 2)}, {"alice-3s.mkv", makeVideo(t, "mkv", "libx264", 3)}}},
		{files: []namedFile{{"bob-4s.webm", makeVideo(t, "webm", "libvpx", 4)}, {"bob-1s.mp4", makeMP4(t, 1)}}},
	}
	wantFrames := map[string]int{"alice-2s.mp4": 2, "alice-3s.mkv": 3, "bob-4s.webm": 4, "bob-1s.mp4": 1}
	for _, u := range uploads {
		_, u.token = registerAndLogin(t)
	}

	// Both requests are released together.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, u := range uploads {
		wg.Go(func() {
			<-start
			u.resp, u.body, u.err = postVideos(u.token, u.files...)
		})
	}
	close(start)
	wg.Wait()

	for _, u := range uploads {
		if u.err != nil {
			t.Fatalf("upload of %v failed: %v", u.files[0].name, u.err)
		}
		ids := videoIDs(acceptedVideos(t, u.resp, u.body, u.files...))
		final := waitForFinalStatuses(t, u.token, ids, 500*time.Millisecond, processingTimeout(t), nil)
		if page := listVideos(t, u.token, ""); page.Total != len(u.files) {
			t.Errorf("list total = %d, want %d (only the user's own videos)", page.Total, len(u.files))
		}
		for _, id := range ids {
			v := final[id]
			if v.Status != statusDone {
				t.Errorf("video %s ended %s, want DONE: %s", v.OriginalName, v.Status, describe(v))
				continue
			}
			want := wantFrames[v.OriginalName]
			if *v.FrameCount != want {
				t.Errorf("video %s: frame_count = %d, want %d", v.OriginalName, *v.FrameCount, want)
			}
			_, zipData := downloadZip(t, u.token, id)
			assertFrames(t, zipData, want)
		}
	}
}
