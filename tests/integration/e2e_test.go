package integration

// End-to-end test of the flow the web UI drives: sign up, log in, upload
// several videos in one request, poll the list until every video is final,
// download the frames of the DONE ones and receive the failure e-mail of
// the FAILED one.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEndToEnd(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	// Sign up and log in.
	user, token := registerAndLogin(t)

	// Upload two good videos of different lengths and a corrupt one.
	files := []namedFile{
		{"short clip.mp4", makeMP4(t, 2)},
		{"longer clip.webm", makeVideo(t, "webm", "libvpx", 4)},
		{"broken.avi", corruptVideo()},
	}
	wantFrames := map[string]int{"short clip.mp4": 2, "longer clip.webm": 4}
	uploaded := mustUpload(t, token, files...)
	ids := videoIDs(uploaded)

	// Poll the list, as the UI does, until every video is final.
	waitForFinalStatuses(t, token, ids, time.Second, processingTimeout(t), nil)

	page := listVideos(t, token, "")
	if page.Total != len(files) || len(page.Items) != len(files) {
		t.Fatalf("list: total %d with %d items, want %d", page.Total, len(page.Items), len(files))
	}
	listed := map[string]video{}
	for _, v := range page.Items {
		listed[v.ID] = v
	}
	var failed video
	for _, id := range ids {
		v, ok := listed[id]
		if !ok {
			t.Fatalf("uploaded video %s is not listed", id)
		}
		if v.OriginalName == "broken.avi" {
			if v.Status != statusFailed {
				t.Fatalf("broken.avi ended %s, want FAILED: %s", v.Status, describe(v))
			}
			failed = v
			// Download of a FAILED video is refused.
			resp, body := authGet(t, token, downloadPath(id))
			assertError(t, resp, body, http.StatusConflict, "video_not_ready")
			continue
		}
		want := wantFrames[v.OriginalName]
		if v.Status != statusDone || *v.FrameCount != want {
			t.Errorf("%s: want DONE with %d frames, got %s", v.OriginalName, want, describe(v))
			continue
		}
		// Download the frames.
		_, zipData := downloadZip(t, token, id)
		assertFrames(t, zipData, want)
	}

	// The owner is told about the failure by e-mail.
	mails := waitForMails(t, user.Email, 1)
	if len(mails) != 1 {
		t.Errorf("%s received %d e-mails, want 1; subjects: %q", user.Email, len(mails), subjects(mails))
	}
	mail := mails[0]
	for _, m := range mails {
		if strings.Contains(m.Subject, failed.OriginalName) {
			mail = m
		}
	}
	assertFailureMail(t, mail, user.Email, failed)
}
