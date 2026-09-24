// Package metrics sets up the Prometheus metrics of a service
// (docs/observability.md, "Metrics"): a registry per service with the Go
// runtime and process collectors and videoproc_build_info, and the handler
// that serves it at GET /metrics. The service's own collectors
// (internal/adapters/http for HTTP, internal/adapters/prom for the rest)
// are registered on the same registry.
package metrics

import (
	"net/http"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace prefixes every metric of the video processor.
const Namespace = "videoproc"

// Path is where the metrics are served.
const Path = "/metrics"

// Version is the version reported by videoproc_build_info. The images set
// it at build time (-ldflags "-X video-processor/internal/platform/
// metrics.Version=..."); when empty, the VCS revision recorded by the Go
// toolchain is used, or "dev".
var Version string

// version resolves Version.
func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value[:min(len(s.Value), 12)]
			}
		}
	}
	return "dev"
}

// NewRegistry returns the registry of service (api, worker or notifier)
// with the Go runtime and process collectors and videoproc_build_info
// {service, version}, a constant 1. Services use it instead of
// prometheus.DefaultRegisterer, so tests and several services in one
// process never share collectors.
func NewRegistry(service string) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   Namespace,
			Name:        "build_info",
			Help:        "Build of the service; always 1.",
			ConstLabels: prometheus.Labels{"service": service, "version": version()},
		}, func() float64 { return 1 }),
	)
	return reg
}

// Handler serves the metrics of reg in the Prometheus text (or
// OpenMetrics, when asked) format. A collector that fails is left out of
// the answer instead of failing the whole scrape.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg, // promhttp_metric_handler_errors_total
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	})
}

// Mux returns a handler serving the metrics of reg at Path and next at
// every other path. The worker and the notifier serve it on their probe
// listener (HEALTH_ADDR); the /metrics requests do not reach next, so they
// are not in the access log.
func Mux(reg *prometheus.Registry, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+Path, Handler(reg))
	if next != nil {
		mux.Handle("/", next)
	}
	return mux
}
