package integration

// Integration tests for RF2, "never lose a request during load peaks"
// (docs/openapi.yaml: "A 202 means the job is durably queued and will not be
// lost").

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// burstSize is the number of concurrent uploads of TestBurstOfUploadsIsNotLost:
// BURST_SIZE or 20.
func burstSize(t *testing.T) int {
	t.Helper()
	v := os.Getenv("BURST_SIZE")
	if v == "" {
		return 20
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("invalid BURST_SIZE %q: want a positive integer", v)
	}
	return n
}

// burstTimeout bounds the wait for a whole burst to be processed:
// BURST_TIMEOUT (a Go duration) or twice processingTimeout, since the burst
// queues many videos at once.
func burstTimeout(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv("BURST_TIMEOUT")
	if v == "" {
		return 2 * processingTimeout(t)
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("invalid BURST_TIMEOUT %q: %v", v, err)
	}
	return d
}

// TestBurstOfUploadsIsNotLost releases BURST_SIZE single-file uploads from
// several users at the same instant. Every request must be accepted (202;
// no 5xx, no timeout or dropped connection), and every accepted video must
// be processed to DONE and listed exactly once to its owner.
func TestBurstOfUploadsIsNotLost(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	m := burstSize(t)
	tokens := make([]string, min(4, m))
	for i := range tokens {
		_, tokens[i] = registerAndLogin(t)
	}
	data := makeMP4(t, 1)

	type result struct {
		user int
		file namedFile
		resp *http.Response
		body []byte
		err  error
	}
	results := make([]result, m)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		r := &results[i]
		r.user, r.file = i%len(tokens), namedFile{fmt.Sprintf("burst-%03d.mp4", i), data}
		wg.Go(func() {
			<-start
			r.resp, r.body, r.err = postVideos(tokens[r.user], r.file)
		})
	}
	close(start)
	wg.Wait()

	accepted := make([][]string, len(tokens))
	for _, r := range results {
		switch {
		case r.err != nil:
			t.Errorf("upload %s got no response: %v", r.file.name, r.err)
		case r.resp.StatusCode != http.StatusAccepted:
			t.Errorf("upload %s: expected 202, got %d: %s", r.file.name, r.resp.StatusCode, r.body)
		default:
			v := acceptedVideos(t, r.resp, r.body, r.file)[0]
			accepted[r.user] = append(accepted[r.user], v.ID)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	timeout := burstTimeout(t)
	for user, token := range tokens {
		ids := accepted[user]
		final := waitForFinalStatuses(t, token, ids, time.Second, timeout, nil)
		for _, id := range ids {
			if v := final[id]; v.Status != statusDone || *v.FrameCount != 1 {
				t.Errorf("video %s ended %s, want DONE with 1 frame: %s", v.OriginalName, v.Status, describe(v))
			}
		}
		assertListedOnce(t, token, ids)
	}
}

// assertListedOnce fails unless the caller's list holds exactly the videos
// ids, each once.
func assertListedOnce(t *testing.T, token string, ids []string) {
	t.Helper()
	listed := listAll(t, token)
	if len(listed) != len(ids) {
		t.Errorf("list has %d videos, want the %d uploaded", len(listed), len(ids))
	}
	seen := map[string]int{}
	for _, v := range listed {
		seen[v.ID]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("video %s is listed %d times, want once", id, seen[id])
		}
	}
}

// TestWorkerRestartLosesNoVideo restarts the worker service while videos
// are PROCESSING and queued; every one must still end DONE. A crash or
// restart may make a video be processed again, but never lost.
//
// It needs the compose stack started by TestMain, the only case in which the
// suite controls the stack: against BASE_URL it is skipped, as an
// environment precondition. The service is WORKER_SERVICE or "worker". Not
// parallel with other tests, so the restart does not slow down their
// videos.
func TestWorkerRestartLosesNoVideo(t *testing.T) {
	notImplemented(t)
	if startedStack == nil {
		if baseURL == "" {
			t.Fatal(errNoApp)
		}
		t.Skip("precondition: restarting the worker needs the compose stack started by TestMain; with BASE_URL set the suite does not control the stack")
	}
	service := envOr("WORKER_SERVICE", "worker")
	const n = 8
	_, token := registerAndLogin(t)
	data := makeSlowVideo(t, slowVideoSeconds)
	files := make([]namedFile, n)
	for i := range files {
		files[i] = namedFile{fmt.Sprintf("restart-%d.mp4", i+1), data}
	}
	ids := videoIDs(mustUpload(t, token, files...))

	restarted := false
	final := waitForFinalStatuses(t, token, ids, 200*time.Millisecond, processingTimeout(t), func(snapshot map[string]video) {
		if restarted {
			return
		}
		for _, id := range ids {
			if snapshot[id].Status == statusProcessing {
				if err := startedStack.compose("restart", service); err != nil {
					t.Fatalf("docker compose restart %s: %v", service, err)
				}
				restarted = true
				return
			}
		}
	})

	if !restarted {
		t.Fatalf("no video was seen PROCESSING, so the %s service was never restarted mid-run", service)
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
	assertListedOnce(t, token, ids)
}
