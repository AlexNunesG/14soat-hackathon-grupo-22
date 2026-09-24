package prom

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/app"
	"video-processor/internal/platform/metrics"
)

// List cache results (label result of videoproc_list_cache_requests_total).
const (
	cacheHit   = "hit"
	cacheMiss  = "miss"
	cacheError = "error"
)

// InstrumentListCache registers videoproc_list_cache_requests_total on reg
// and returns cache counting its lookups. app.Videos.List does one lookup
// per request: ListVersion, then GetList unless ListVersion failed; so
// each request counts once, as hit, miss, or error (either call failed:
// the database answered).
func InstrumentListCache(reg prometheus.Registerer, cache app.VideoListCache) app.VideoListCache {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metrics.Namespace,
		Name:      "list_cache_requests_total",
		Help:      "Lookups of the video list cache (GET /api/v1/videos), by result: hit, miss, error (Redis failed; the database answered).",
	}, []string{"result"})
	reg.MustRegister(requests)
	for _, r := range []string{cacheHit, cacheMiss, cacheError} {
		requests.WithLabelValues(r)
	}
	return &countingCache{VideoListCache: cache, requests: requests}
}

type countingCache struct {
	app.VideoListCache
	requests *prometheus.CounterVec
}

func (c *countingCache) ListVersion(ctx context.Context, ownerID string) (string, error) {
	v, err := c.VideoListCache.ListVersion(ctx, ownerID)
	if err != nil {
		c.requests.WithLabelValues(cacheError).Inc()
	}
	return v, err
}

func (c *countingCache) GetList(ctx context.Context, ownerID, version string, page app.Page) (app.VideoPage, bool, error) {
	vp, hit, err := c.VideoListCache.GetList(ctx, ownerID, version, page)
	switch {
	case err != nil:
		c.requests.WithLabelValues(cacheError).Inc()
	case hit:
		c.requests.WithLabelValues(cacheHit).Inc()
	default:
		c.requests.WithLabelValues(cacheMiss).Inc()
	}
	return vp, hit, err
}
