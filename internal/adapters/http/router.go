// Package httpapi is the HTTP adapter of the api service (Gin): routes,
// handlers, middleware and the error envelope of docs/openapi.yaml. It
// lives in internal/adapters/http (ADR 0002); the package is named httpapi
// so it does not shadow net/http.
package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/app"
)

// DefaultCheckTimeout bounds each readiness check when Options leaves it
// zero.
const DefaultCheckTimeout = 2 * time.Second

// Options configures the router.
type Options struct {
	Logger *slog.Logger
	// Checks are the dependencies GET /readyz reports, by name.
	Checks []Check
	// CheckTimeout bounds each readiness check.
	CheckTimeout time.Duration

	// Auth serves sign-up and login.
	Auth AuthService
	// Tokens verifies the bearer tokens of the /api/v1/videos* routes.
	Tokens app.TokenVerifier
	// Videos serves the caller's videos.
	Videos VideoService
	// Uploads creates videos from uploads.
	Uploads UploadService
	// MaxUploadBytes limits the size of an upload request; 0 means
	// DefaultMaxUploadBytes.
	MaxUploadBytes int64
	// UploadTempDir is where uploads are spooled while they are received;
	// "" means os.TempDir().
	UploadTempDir string
	// WebUI serves the web UI at GET / and its assets under /ui/.
	WebUI bool
	// Metrics, when set, records the HTTP and upload metrics
	// (NewMetrics). The metrics are not served by this router: the api
	// serves them on a separate internal listener (METRICS_ADDR).
	Metrics *Metrics
}

func init() {
	// Gin's debug mode prints route tables and warnings to stdout; the
	// service logs JSON through slog instead.
	gin.SetMode(gin.ReleaseMode)
}

// NewRouter returns the api's HTTP handler.
func NewRouter(opts Options) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	timeout := opts.CheckTimeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}

	maxUpload := opts.MaxUploadBytes
	if maxUpload <= 0 {
		maxUpload = DefaultMaxUploadBytes
	}

	r := newEngine(log, opts.Checks, timeout, opts.Metrics)
	v1 := r.Group("/api/v1")
	v1.POST("/auth/register", register(log, opts.Auth))
	v1.POST("/auth/login", login(log, opts.Auth))

	// Every video route requires a bearer token (docs/openapi.yaml).
	videos := v1.Group("/videos", requireAuth(log, opts.Tokens), videoIDField())
	videos.GET("", listVideos(log, opts.Videos))
	videos.POST("", uploadVideos(log, opts.Uploads, maxUpload, opts.UploadTempDir, opts.Metrics))
	videos.GET("/:id", getVideo(log, opts.Videos))
	videos.GET("/:id/download", downloadVideo(log, opts.Videos))
	if opts.WebUI {
		mountWebUI(r)
	}
	return r
}

// NewHealthRouter returns a handler with only the probes GET /healthz and
// GET /readyz, for services without an API (the worker).
func NewHealthRouter(log *slog.Logger, checks []Check, checkTimeout time.Duration) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if checkTimeout <= 0 {
		checkTimeout = DefaultCheckTimeout
	}
	return newEngine(log, checks, checkTimeout, nil)
}

// newEngine returns an engine with the middleware, the 404 envelope and the
// probes; m (optional) records the HTTP metrics.
func newEngine(log *slog.Logger, checks []Check, checkTimeout time.Duration, m *Metrics) *gin.Engine {
	r := gin.New()
	r.ContextWithFallback = true // c.Done()/c.Err() follow the request context
	r.Use(requestID(), requestLogger(log))
	if m != nil {
		// After the access log and before the recoverer, so a panic is
		// counted as the 500 it is answered with.
		r.Use(m.middleware())
	}
	r.Use(recoverer(log))
	r.NoRoute(func(c *gin.Context) {
		WriteError(c, http.StatusNotFound, CodeNotFound, "route not found")
	})
	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(log, checks, checkTimeout))
	return r
}

// recoverer turns a panic into a logged 500 with the error envelope.
func recoverer(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, rec any) {
		log.ErrorContext(c.Request.Context(), "panic serving request",
			slog.String("route", routeOf(c)), slog.String("panic", fmt.Sprint(rec)))
		WriteError(c, http.StatusInternalServerError, CodeInternal, "internal server error")
	})
}
