// Package prom holds the Prometheus collectors of the video processor's
// use cases and outbound adapters (docs/observability.md, "Metrics"): it
// implements the metrics ports of internal/app (app.ProcessorMetrics,
// app.OutcomeRecorder) and of the consumer (rabbitmq.Metrics, by its
// method set), and decorates app ports (MessagePublisher, Mailer,
// VideoListCache) to measure them. The registry and GET /metrics are in
// internal/platform/metrics; the HTTP metrics in internal/adapters/http.
//
// Labels are bounded: outcomes and results are fixed sets, never ids,
// e-mail addresses or paths.
package prom

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/app"
	"video-processor/internal/platform/metrics"
)

// Worker holds the worker's job metrics. It implements
// app.ProcessorMetrics (outcomes decided by the processor) and
// rabbitmq.Metrics (outcomes decided by the consumer, and the jobs in
// progress).
type Worker struct {
	jobs       *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	inProgress prometheus.Gauge
	frames     prometheus.Counter
}

// JobOutcomes are the values of the outcome label of the job metrics.
var JobOutcomes = []app.Outcome{
	app.OutcomeDone, app.OutcomeFailed, app.OutcomeIgnored,
	app.OutcomeRetried, app.OutcomeDeadLettered, app.OutcomeRequeued,
}

// jobDurationBuckets go from 100 ms to 20 min (FFMPEG_TIMEOUT is 10 min by
// default).
var jobDurationBuckets = []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200}

// NewWorker registers the job metrics on reg.
func NewWorker(reg prometheus.Registerer) *Worker {
	w := &Worker{
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "jobs_total",
			Help:      "Processing jobs (video.process deliveries) handled, by outcome: done, failed (the video cannot be processed), ignored (nothing to do), retried, dead_lettered (retries exhausted: the video ends FAILED; or malformed), requeued.",
		}, []string{"outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "job_duration_seconds",
			Help:      "Time to handle one processing job (download, ffmpeg, zip, upload, status change), by outcome.",
			Buckets:   jobDurationBuckets,
		}, []string{"outcome"}),
		inProgress: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "jobs_in_progress",
			Help:      "Processing jobs being handled by this worker.",
		}),
		frames: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "frames_extracted_total",
			Help:      "Frames extracted from the videos that ended DONE.",
		}),
	}
	reg.MustRegister(w.jobs, w.duration, w.inProgress, w.frames)
	for _, o := range JobOutcomes { // every series exists from the start
		w.jobs.WithLabelValues(string(o))
		w.duration.WithLabelValues(string(o))
	}
	return w
}

// RecordOutcome implements app.OutcomeRecorder.
func (w *Worker) RecordOutcome(outcome app.Outcome, d time.Duration) {
	w.jobs.WithLabelValues(string(outcome)).Inc()
	w.duration.WithLabelValues(string(outcome)).Observe(d.Seconds())
}

// FramesExtracted implements app.ProcessorMetrics.
func (w *Worker) FramesExtracted(n int) { w.frames.Add(float64(n)) }

// InFlight implements rabbitmq.Metrics.
func (w *Worker) InFlight(delta int) { w.inProgress.Add(float64(delta)) }

// Notifier holds the notifier's metrics. It implements app.OutcomeRecorder
// and rabbitmq.Metrics; InstrumentMailer measures the SMTP sends.
type Notifier struct {
	notifications *prometheus.CounterVec
	inProgress    prometheus.Gauge
	smtp          *prometheus.HistogramVec
}

// NotificationResults are the values of the result label of
// videoproc_notifications_total.
var NotificationResults = []app.Outcome{
	app.OutcomeSent, app.OutcomeDuplicate, app.OutcomeIgnored,
	app.OutcomeRetried, app.OutcomeDeadLettered, app.OutcomeRequeued,
}

// SMTP send results (label result of videoproc_smtp_send_duration_seconds).
const (
	resultOK    = "ok"
	resultError = "error"
)

// NewNotifier registers the notifier's metrics on reg.
func NewNotifier(reg prometheus.Registerer) *Notifier {
	n := &Notifier{
		notifications: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Name:      "notifications_total",
			Help:      "Video events (video.notify deliveries) handled, by result: sent, duplicate (already sent), ignored (nothing to notify), retried, dead_lettered, requeued.",
		}, []string{"result"}),
		inProgress: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Name:      "notifications_in_progress",
			Help:      "Video events being handled by this notifier.",
		}),
		smtp: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Name:      "smtp_send_duration_seconds",
			Help:      "Time to send one e-mail through SMTP, by result (ok, error).",
			Buckets:   []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"result"}),
	}
	reg.MustRegister(n.notifications, n.inProgress, n.smtp)
	for _, r := range NotificationResults {
		n.notifications.WithLabelValues(string(r))
	}
	n.smtp.WithLabelValues(resultOK)
	n.smtp.WithLabelValues(resultError)
	return n
}

// RecordOutcome implements app.OutcomeRecorder. The duration is not
// recorded: the SMTP send, which dominates it, has its own histogram.
func (n *Notifier) RecordOutcome(outcome app.Outcome, _ time.Duration) {
	n.notifications.WithLabelValues(string(outcome)).Inc()
}

// InFlight implements rabbitmq.Metrics.
func (n *Notifier) InFlight(delta int) { n.inProgress.Add(float64(delta)) }

// InstrumentMailer returns m timing every Send in
// videoproc_smtp_send_duration_seconds.
func (n *Notifier) InstrumentMailer(m app.Mailer) app.Mailer {
	return &timedMailer{next: m, hist: n.smtp}
}

type timedMailer struct {
	next app.Mailer
	hist *prometheus.HistogramVec
}

func (t *timedMailer) Send(ctx context.Context, m app.Mail) error {
	start := time.Now()
	err := t.next.Send(ctx, m)
	result := resultOK
	if err != nil {
		result = resultError
	}
	t.hist.WithLabelValues(result).Observe(time.Since(start).Seconds())
	return err
}
