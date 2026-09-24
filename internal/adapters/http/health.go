package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger is a dependency that can be checked for readiness.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingFunc adapts a function to Pinger.
type PingFunc func(ctx context.Context) error

// Ping calls f.
func (f PingFunc) Ping(ctx context.Context) error { return f(ctx) }

// Check is one named dependency of GET /readyz, e.g. "database".
type Check struct {
	Name   string
	Pinger Pinger
}

// Check results, as reported in the readiness body.
const (
	statusOK    = "ok"
	statusError = "error"
)

type livenessBody struct {
	Status string `json:"status"`
}

type readinessBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// healthz reports that the process is up. It checks no dependency.
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, livenessBody{Status: statusOK})
}

// readyz runs every check concurrently, each bounded by timeout, and
// answers 200 when all pass or 503 naming the failing ones. Failure details
// are logged, never returned.
func readyz(log *slog.Logger, checks []Check, timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, chk := range checks {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
				defer cancel()
				result := statusOK
				if err := chk.Pinger.Ping(ctx); err != nil {
					result = statusError
					log.WarnContext(ctx, "readiness check failed", slog.String("check", chk.Name), slog.Any("error", err))
				}
				mu.Lock()
				results[chk.Name] = result
				mu.Unlock()
			})
		}
		wg.Wait()

		body := readinessBody{Status: statusOK, Checks: results}
		code := http.StatusOK
		for _, r := range results {
			if r != statusOK {
				body.Status, code = statusError, http.StatusServiceUnavailable
				break
			}
		}
		c.JSON(code, body)
	}
}
