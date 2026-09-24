package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/app"
)

// metricsAPI returns the api router recording its metrics on a fresh
// registry.
func metricsAPI(t *testing.T, uploads *fakeUploads) (http.Handler, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	h := httpapi.NewRouter(httpapi.Options{
		Tokens: fakeTokens{}, Videos: &fakeVideoService{err: app.ErrNotFound}, Uploads: uploads,
		UploadTempDir: t.TempDir(), Metrics: httpapi.NewMetrics(reg),
	})
	return h, reg
}

// family returns the metric family name from reg.
func family(t *testing.T, reg prometheus.Gatherer, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// labelsOf renders the labels of m as name=value pairs, sorted by name.
func labelsOf(m *dto.Metric) string {
	var parts []string
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return strings.Join(parts, ",")
}

func TestMetricsCountRequestsByRoutePattern(t *testing.T) {
	h, reg := metricsAPI(t, &fakeUploads{})
	id := "33333333-3333-4333-8333-333333333333"
	serve(h, http.MethodGet, "/api/v1/videos/"+videoID, aliceToken, "")
	serve(h, http.MethodGet, "/api/v1/videos/"+id, aliceToken, "")
	serve(h, http.MethodGet, "/api/v1/videos/"+id, "", "") // 401
	serve(h, http.MethodGet, "/no/such/path/"+id, "", "")
	serve(h, "PROPFIND", "/no/such/path", "", "")
	serve(h, http.MethodGet, "/healthz", "", "")

	counts := map[string]float64{}
	f := family(t, reg, "videoproc_http_requests_total")
	if f == nil {
		t.Fatal("videoproc_http_requests_total not exported")
	}
	for _, m := range f.GetMetric() {
		counts[labelsOf(m)] = m.GetCounter().GetValue()
	}
	want := map[string]float64{
		"method=GET,route=/api/v1/videos/:id,status=404": 2,
		"method=GET,route=/api/v1/videos/:id,status=401": 1,
		"method=GET,route=unmatched,status=404":          1,
		"method=OTHER,route=unmatched,status=404":        1,
		"method=GET,route=/healthz,status=200":           1,
	}
	if len(counts) != len(want) {
		t.Errorf("series %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s = %v, want %v (all: %v)", k, counts[k], v, counts)
		}
	}

	// The duration histogram has one observation per request, by route.
	d := family(t, reg, "videoproc_http_request_duration_seconds")
	if d == nil {
		t.Fatal("videoproc_http_request_duration_seconds not exported")
	}
	durations := map[string]uint64{}
	for _, m := range d.GetMetric() {
		durations[labelsOf(m)] = m.GetHistogram().GetSampleCount()
	}
	if durations["method=GET,route=/api/v1/videos/:id"] != 3 || durations["method=GET,route=unmatched"] != 1 {
		t.Errorf("duration samples %v", durations)
	}
	for labels := range durations {
		if strings.Contains(labels, id) || strings.Contains(labels, videoID) {
			t.Errorf("an id leaked into the labels: %s", labels)
		}
	}
	f = family(t, reg, "videoproc_http_requests_in_flight")
	if f == nil || len(f.GetMetric()) != 1 || f.GetMetric()[0].GetGauge().GetValue() != 0 {
		t.Errorf("in flight after the requests: %v, want 0", f)
	}
}

func TestMetricsCountInFlightRequests(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	inside := make(chan float64, 1)
	videos := &observingVideos{observe: func() {
		f := family(t, reg, "videoproc_http_requests_in_flight")
		inside <- f.GetMetric()[0].GetGauge().GetValue()
	}}
	h := httpapi.NewRouter(httpapi.Options{
		Tokens: fakeTokens{}, Videos: videos, Uploads: &fakeUploads{}, Metrics: httpapi.NewMetrics(reg),
	})
	serve(h, http.MethodGet, "/api/v1/videos", aliceToken, "")
	if got := <-inside; got != 1 {
		t.Errorf("in flight during the request = %v, want 1", got)
	}
}

// observingVideos calls observe when listing.
type observingVideos struct {
	fakeVideoService
	observe func()
}

func (v *observingVideos) List(ctx context.Context, ownerID string, page app.Page) (app.VideoPage, error) {
	v.observe()
	return v.fakeVideoService.List(ctx, ownerID, page)
}

func TestMetricsCountPanicsAs500(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	h := httpapi.NewRouter(httpapi.Options{
		Tokens: fakeTokens{}, Videos: &panickingVideos{}, Uploads: &fakeUploads{},
		Metrics: httpapi.NewMetrics(reg),
	})
	rec := serve(h, http.MethodGet, "/api/v1/videos", aliceToken, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	f := family(t, reg, "videoproc_http_requests_total")
	if f == nil || len(f.GetMetric()) != 1 || labelsOf(f.GetMetric()[0]) != "method=GET,route=/api/v1/videos,status=500" {
		t.Errorf("series %v", f)
	}
}

// panickingVideos is a VideoService whose List panics.
type panickingVideos struct{ fakeVideoService }

func (*panickingVideos) List(context.Context, string, app.Page) (app.VideoPage, error) {
	panic("boom")
}

func TestMetricsCountUploadsByResult(t *testing.T) {
	uploads := &fakeUploads{}
	h, reg := metricsAPI(t, uploads)

	body, ct := multipartBody(t, part{"videos", "a.mp4", "12345"}, part{"videos", "b.mkv", "123"})
	if rec := postUpload(h, body, ct); rec.Code != http.StatusAccepted {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	body, ct = multipartBody(t, part{"videos", "notes.txt", "x"})
	if rec := postUpload(h, body, ct); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format: %d", rec.Code)
	}
	body, ct = multipartBody(t, part{"other", "", "x"})
	if rec := postUpload(h, body, ct); rec.Code != http.StatusBadRequest {
		t.Fatalf("no file: %d", rec.Code)
	}

	results := map[string]float64{}
	for _, m := range family(t, reg, "videoproc_uploads_total").GetMetric() {
		results[labelsOf(m)] = m.GetCounter().GetValue()
	}
	want := map[string]float64{"result=accepted": 1, "result=unsupported_format": 1, "result=missing_file": 1}
	if len(results) != len(want) {
		t.Errorf("uploads %v, want %v", results, want)
	}
	for k, v := range want {
		if results[k] != v {
			t.Errorf("%s = %v, want %v", k, results[k], v)
		}
	}

	sizes := family(t, reg, "videoproc_upload_bytes").GetMetric()[0].GetHistogram()
	if sizes.GetSampleCount() != 2 || sizes.GetSampleSum() != 8 {
		t.Errorf("upload sizes: %d files, %v bytes; want 2 and 8", sizes.GetSampleCount(), sizes.GetSampleSum())
	}
}

func TestNoMetricsWithoutRegistry(t *testing.T) {
	// A router without Metrics works (NewHealthRouter, older wiring).
	h := httpapi.NewRouter(httpapi.Options{Tokens: fakeTokens{}, Videos: &fakeVideoService{}, Uploads: &fakeUploads{}, UploadTempDir: t.TempDir()})
	body, ct := multipartBody(t, part{"videos", "a.mp4", "1"})
	if rec := postUpload(h, body, ct); rec.Code != http.StatusAccepted {
		t.Fatalf("upload: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /metrics on the API router: %d, want 404 (served on METRICS_ADDR only)", rec.Code)
	}
}
