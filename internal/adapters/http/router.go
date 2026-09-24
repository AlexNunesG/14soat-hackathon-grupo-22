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

	r := gin.New()
	r.ContextWithFallback = true // c.Done()/c.Err() follow the request context
	r.Use(requestLogger(log), recoverer(log))
	r.NoRoute(func(c *gin.Context) {
		WriteError(c, http.StatusNotFound, CodeNotFound, "route not found")
	})

	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(log, opts.Checks, timeout))
	return r
}

// requestLogger logs one line per request. Probe requests are logged at
// debug level so they don't flood the logs.
func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		level := slog.LevelInfo
		switch path := c.Request.URL.Path; {
		case path == "/healthz" || path == "/readyz":
			// A failing readiness check is already logged by readyz.
			level = slog.LevelDebug
		case c.Writer.Status() >= http.StatusInternalServerError:
			level = slog.LevelError
		}
		log.LogAttrs(c.Request.Context(), level, "http request",
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote", c.ClientIP()),
		)
	}
}

// recoverer turns a panic into a logged 500 with the error envelope.
func recoverer(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, rec any) {
		log.ErrorContext(c.Request.Context(), "panic serving request",
			slog.String("path", c.Request.URL.Path), slog.String("panic", fmt.Sprint(rec)))
		WriteError(c, http.StatusInternalServerError, CodeInternal, "internal server error")
	})
}
