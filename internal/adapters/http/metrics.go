package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/app"
	"video-processor/internal/platform/metrics"
)

// Metrics are the Prometheus collectors of the api's HTTP adapter
// (docs/observability.md, "Metrics"). Labels are bounded: the route is the
// route pattern (never the raw path, which holds ids), "unmatched" for
// unknown paths, and methods outside the standard ones are "OTHER". A nil
// *Metrics records nothing.
type Metrics struct {
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	inFlight    prometheus.Gauge
	uploads     *prometheus.CounterVec
	uploadBytes prometheus.Histogram
}

// Upload results besides the error codes (ErrorCode) of rejected uploads.
const (
	uploadAccepted = "accepted"
	// uploadInternal is a request that failed without an error code (a
	// panic): it ends as a 500.
	uploadInternal = string(CodeInternal)
)

// NewMetrics registers the HTTP collectors on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "http_requests_total",
			Help:      "HTTP requests served, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "http_request_duration_seconds",
			Help:      "Time to serve an HTTP request (uploads and downloads included), by method and route pattern.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "http_requests_in_flight",
			Help:      "HTTP requests being served.",
		}),
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "uploads_total",
			Help:      "Upload requests (POST /api/v1/videos) by result: accepted, or the error code of the rejection.",
		}, []string{"result"}),
		uploadBytes: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "upload_bytes",
			Help:      "Size of each accepted video file; _count is the videos accepted, _sum their bytes.",
			// 64 KiB to 4 GiB.
			Buckets: prometheus.ExponentialBuckets(64<<10, 4, 9),
		}),
	}
	reg.MustRegister(m.requests, m.duration, m.inFlight, m.uploads, m.uploadBytes)
	return m
}

// standardMethods are the methods kept as a label value.
var standardMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// methodLabel bounds the method label: a client can send any token.
func methodLabel(method string) string {
	if standardMethods[method] {
		return method
	}
	return "OTHER"
}

// middleware counts and times the requests, by route pattern.
func (m *Metrics) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		m.inFlight.Inc()
		defer m.inFlight.Dec()
		c.Next()
		method, route := methodLabel(c.Request.Method), routeOf(c)
		m.requests.WithLabelValues(method, route, strconv.Itoa(c.Writer.Status())).Inc()
		m.duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	}
}

// recordUpload records the result of an upload request once its handler
// is done: accepted (202) with the size of every file, or the error code
// it was rejected with.
func (m *Metrics) recordUpload(c *gin.Context, files []app.UploadFile) {
	if m == nil {
		return
	}
	if c.Writer.Status() == http.StatusAccepted {
		m.uploads.WithLabelValues(uploadAccepted).Inc()
		for _, f := range files {
			if f.Size >= 0 {
				m.uploadBytes.Observe(float64(f.Size))
			}
		}
		return
	}
	result := uploadInternal
	if code, ok := c.Get(errorCodeKey); ok {
		if code, ok := code.(ErrorCode); ok {
			result = string(code)
		}
	}
	m.uploads.WithLabelValues(result).Inc()
}
