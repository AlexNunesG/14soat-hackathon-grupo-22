package prom

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/app"
	"video-processor/internal/platform/metrics"
)

// pendingTimeout bounds the query of videoproc_outbox_pending during a
// scrape.
const pendingTimeout = 2 * time.Second

// Outbox holds the metrics of an outbox relay (ADR 0004); the api and the
// worker both run one.
type Outbox struct {
	published *prometheus.CounterVec
}

// PendingFunc counts the messages waiting in the outbox
// (postgres.Outbox.Pending).
type PendingFunc func(ctx context.Context) (int64, error)

// NewOutbox registers the outbox metrics on reg:
// videoproc_outbox_published_total, counted by Publisher, and
// videoproc_outbox_pending, read with pending at every scrape. The outbox
// is shared, so every service reports the same pending count.
func NewOutbox(reg prometheus.Registerer, pending PendingFunc) *Outbox {
	o := &Outbox{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "outbox_published_total",
			Help:      "Outbox messages this relay tried to publish, by result: ok (confirmed by the broker) or error (kept in the outbox and retried later).",
		}, []string{"result"}),
	}
	o.published.WithLabelValues(resultOK)
	o.published.WithLabelValues(resultError)
	reg.MustRegister(o.published, &pendingCollector{
		count: pending,
		desc: prometheus.NewDesc(prometheus.BuildFQName(metrics.Namespace, "", "outbox_pending"),
			"Messages waiting in the outbox (not published yet, due or delayed after a failed publish).", nil, nil),
	})
	return o
}

// Publisher returns p counting every message it publishes.
func (o *Outbox) Publisher(p app.MessagePublisher) app.MessagePublisher {
	return &countingPublisher{next: p, published: o.published}
}

type countingPublisher struct {
	next      app.MessagePublisher
	published *prometheus.CounterVec
}

func (c *countingPublisher) Publish(ctx context.Context, msgs []app.Message) []error {
	errs := c.next.Publish(ctx, msgs)
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
		}
	}
	c.published.WithLabelValues(resultOK).Add(float64(len(errs) - failed))
	c.published.WithLabelValues(resultError).Add(float64(failed))
	return errs
}

// pendingCollector queries the outbox size at every scrape. When the
// query fails the metric is left out of that scrape (the database being
// down is reported by /readyz and the other metrics).
type pendingCollector struct {
	count PendingFunc
	desc  *prometheus.Desc
}

func (c *pendingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *pendingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), pendingTimeout)
	defer cancel()
	n, err := c.count(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n))
}
