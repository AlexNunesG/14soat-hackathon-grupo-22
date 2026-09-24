package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/platform/metrics"
)

func get(t *testing.T, h http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerServesTextFormatWithBuildInfo(t *testing.T) {
	metrics.Version = "1.2.3"
	t.Cleanup(func() { metrics.Version = "" })
	reg := metrics.NewRegistry("worker")
	c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: metrics.Namespace, Name: "test_total", Help: "A test counter."})
	reg.MustRegister(c)
	c.Add(2)

	rec := get(t, metrics.Handler(reg), "/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q, want the Prometheus text format", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`videoproc_build_info{service="worker",version="1.2.3"} 1`,
		"videoproc_test_total 2",
		"# TYPE go_goroutines gauge",
		"go_memstats_alloc_bytes ",
		"process_start_time_seconds ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestHandlerServesOpenMetricsWhenAsked(t *testing.T) {
	reg := metrics.NewRegistry("api")
	rec := get(t, metrics.Handler(reg), "/metrics", http.Header{"Accept": {"application/openmetrics-text; version=1.0.0"}})
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Errorf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `videoproc_build_info{service="api",version=`) {
		t.Error("no build info")
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	// Test binaries carry no VCS revision.
	rec := get(t, metrics.Handler(metrics.NewRegistry("api")), "/metrics", nil)
	if !strings.Contains(rec.Body.String(), `videoproc_build_info{service="api",version="dev"} 1`) {
		t.Errorf("build info: %s", grep(rec.Body.String(), "build_info"))
	}
}

// failingCollector always fails to collect.
type failingCollector struct{ desc *prometheus.Desc }

func (c failingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }
func (c failingCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.NewInvalidMetric(c.desc, io.ErrUnexpectedEOF)
}

func TestHandlerSkipsFailingCollectors(t *testing.T) {
	reg := metrics.NewRegistry("api")
	reg.MustRegister(failingCollector{prometheus.NewDesc("videoproc_broken", "Broken.", nil, nil)})
	rec := get(t, metrics.Handler(reg), "/metrics", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "videoproc_build_info") {
		t.Errorf("status %d; a failing collector must not fail the scrape", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "videoproc_broken") {
		t.Error("the failing metric was served")
	}
}

func TestMuxServesMetricsBesideTheProbes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next", r.URL.Path)
	})
	h := metrics.Mux(metrics.NewRegistry("notifier"), next)
	if rec := get(t, h, "/metrics", nil); !strings.Contains(rec.Body.String(), `service="notifier"`) {
		t.Errorf("/metrics: %d %.80s", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, "/healthz", nil); rec.Header().Get("X-Next") != "/healthz" {
		t.Errorf("/healthz did not reach the probes: %d", rec.Code)
	}

	only := metrics.Mux(metrics.NewRegistry("api"), nil)
	if rec := get(t, only, "/healthz", nil); rec.Code != http.StatusNotFound {
		t.Errorf("metrics-only mux: /healthz %d, want 404", rec.Code)
	}
}

func grep(s, substr string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
