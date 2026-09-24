package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"video-processor/internal/platform/logging"
)

// HeaderRequestID is the header carrying the correlation id of a request
// (docs/observability.md): accepted from the client when valid, generated
// otherwise, and always echoed in the response.
const HeaderRequestID = "X-Request-ID"

// requestID stores the request's correlation id in its context (logging.
// WithRequestID) and echoes it in the X-Request-ID response header. A
// missing or invalid (logging.ValidRequestID) X-Request-ID is replaced by a
// new UUID.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		if !logging.ValidRequestID(id) {
			id = uuid.NewString()
		}
		c.Header(HeaderRequestID, id)
		c.Request = c.Request.WithContext(logging.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

// withLogAttrs adds attrs to the correlation fields of the request's
// context, so every later log line of the request carries them.
func withLogAttrs(c *gin.Context, attrs ...slog.Attr) {
	c.Request = c.Request.WithContext(logging.WithAttrs(c.Request.Context(), attrs...))
}

// videoIDField adds the video id of a /videos/:id route to the
// correlation fields, when it is a UUID (anything else is a 404 and not
// worth logging).
func videoIDField() gin.HandlerFunc {
	return func(c *gin.Context) {
		if id := c.Param("id"); uuid.Validate(id) == nil {
			withLogAttrs(c, slog.String(logging.KeyVideoID, id))
		}
		c.Next()
	}
}

// Routes logged at debug level, so they do not flood the access log: the
// probes (a failing readiness check is already logged by readyz) and the
// web UI's static files (unless they fail with a server error).
var (
	probeRoutes  = map[string]bool{"/healthz": true, "/readyz": true}
	staticRoutes = map[string]bool{"/": true, UIPrefix + "*file": true}
)

// requestLogger writes the access log: one line per request, with the
// route pattern (never the raw path, which holds ids), status, duration and
// response size; the request id and user id come from the context. Server
// errors are logged at error level, probes and static files at debug.
func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route, status := routeOf(c), c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case probeRoutes[route]:
			level = slog.LevelDebug
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case staticRoutes[route]:
			level = slog.LevelDebug
		}
		log.LogAttrs(c.Request.Context(), level, "http request",
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.Int("bytes", max(c.Writer.Size(), 0)),
		)
	}
}

// routeOf is the route pattern of the request, for log lines.
func routeOf(c *gin.Context) string {
	if r := c.FullPath(); r != "" {
		return r
	}
	return "unmatched"
}
