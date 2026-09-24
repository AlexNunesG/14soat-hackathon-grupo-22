package prom_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"video-processor/internal/adapters/prom"
	"video-processor/internal/app"
)

var errBoom = errors.New("boom")

// values returns the value of every series of family name in reg, by
// "label=value,..." (sorted by label name).
func values(t *testing.T, reg prometheus.Gatherer, name string) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			var v float64
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				v = m.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				v = m.GetGauge().GetValue()
			case dto.MetricType_HISTOGRAM:
				v = float64(m.GetHistogram().GetSampleCount())
			default:
				t.Fatalf("%s: unexpected type %v", name, f.GetType())
			}
			out[labels(m)] = v
		}
	}
	return out
}

func labels(m *dto.Metric) string {
	var parts []string
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return strings.Join(parts, ",")
}

func TestWorkerCountsJobsByOutcome(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	w := prom.NewWorker(reg)

	// What the processor and the consumer report for: a good video, a
	// corrupt one, a duplicate job and a transient failure retried.
	w.InFlight(1)
	w.RecordOutcome(app.OutcomeDone, 3*time.Second)
	w.FramesExtracted(12)
	w.RecordOutcome(app.OutcomeFailed, 200*time.Millisecond)
	w.RecordOutcome(app.OutcomeIgnored, time.Millisecond)
	w.RecordOutcome(app.OutcomeRetried, time.Second)
	w.InFlight(1)
	w.InFlight(-1)

	jobs := values(t, reg, "videoproc_jobs_total")
	want := map[string]float64{
		"outcome=done": 1, "outcome=failed": 1, "outcome=ignored": 1, "outcome=retried": 1,
		"outcome=dead_lettered": 0, "outcome=requeued": 0, // present from the start
	}
	if len(jobs) != len(want) {
		t.Errorf("jobs %v, want %v", jobs, want)
	}
	for k, v := range want {
		if got, ok := jobs[k]; !ok || got != v {
			t.Errorf("jobs_total{%s} = %v (present %v), want %v", k, got, ok, v)
		}
	}
	if d := values(t, reg, "videoproc_job_duration_seconds"); d["outcome=done"] != 1 || d["outcome=failed"] != 1 {
		t.Errorf("durations %v", d)
	}
	if f := values(t, reg, "videoproc_frames_extracted_total"); f[""] != 12 {
		t.Errorf("frames %v", f)
	}
	if g := values(t, reg, "videoproc_jobs_in_progress"); g[""] != 1 {
		t.Errorf("in progress %v, want 1", g)
	}
}

func TestNotifierCountsNotificationsAndTimesSMTP(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	n := prom.NewNotifier(reg)
	n.RecordOutcome(app.OutcomeSent, time.Second)
	n.RecordOutcome(app.OutcomeDuplicate, time.Millisecond)
	n.RecordOutcome(app.OutcomeDeadLettered, time.Millisecond)

	var sent []app.Mail
	ok := n.InstrumentMailer(mailerFunc(func(_ context.Context, m app.Mail) error { sent = append(sent, m); return nil }))
	failing := n.InstrumentMailer(mailerFunc(func(context.Context, app.Mail) error { return errBoom }))
	if err := ok.Send(context.Background(), app.Mail{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := failing.Send(context.Background(), app.Mail{ID: "2"}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the mailer's", err)
	}
	if len(sent) != 1 {
		t.Errorf("the decorated mailer sent %d mails", len(sent))
	}

	got := values(t, reg, "videoproc_notifications_total")
	if got["result=sent"] != 1 || got["result=duplicate"] != 1 || got["result=dead_lettered"] != 1 || got["result=retried"] != 0 {
		t.Errorf("notifications %v", got)
	}
	if smtp := values(t, reg, "videoproc_smtp_send_duration_seconds"); smtp["result=ok"] != 1 || smtp["result=error"] != 1 {
		t.Errorf("smtp %v", smtp)
	}
}

type mailerFunc func(context.Context, app.Mail) error

func (f mailerFunc) Send(ctx context.Context, m app.Mail) error { return f(ctx, m) }

type publisherFunc func(context.Context, []app.Message) []error

func (f publisherFunc) Publish(ctx context.Context, msgs []app.Message) []error { return f(ctx, msgs) }

func TestOutboxCountsPublishesAndReadsPending(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	pending, pendingErr := int64(7), error(nil)
	o := prom.NewOutbox(reg, func(context.Context) (int64, error) { return pending, pendingErr })
	p := o.Publisher(publisherFunc(func(_ context.Context, msgs []app.Message) []error {
		return []error{nil, errBoom, nil}[:len(msgs)]
	}))
	if errs := p.Publish(context.Background(), make([]app.Message, 3)); len(errs) != 3 || !errors.Is(errs[1], errBoom) {
		t.Fatalf("errors %v not passed through", errs)
	}
	got := values(t, reg, "videoproc_outbox_published_total")
	if got["result=ok"] != 2 || got["result=error"] != 1 {
		t.Errorf("published %v", got)
	}
	if got := values(t, reg, "videoproc_outbox_pending"); got[""] != 7 {
		t.Errorf("pending %v, want 7", got)
	}

	// A failing count leaves the gauge out of the scrape.
	pendingErr = errBoom
	if _, err := reg.Gather(); err == nil {
		t.Error("Gather: want the count's error")
	}
	if n, err := testutil.GatherAndCount(prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		mfs, _ := reg.Gather()
		return mfs, nil
	}), "videoproc_outbox_pending"); err != nil || n != 0 {
		t.Errorf("pending series with a failing count: %d, %v; want 0", n, err)
	}
}

// fakeCache is an app.VideoListCache with canned results.
type fakeCache struct {
	versionErr error
	hit        bool
	getErr     error
}

func (c *fakeCache) ListVersion(context.Context, string) (string, error) { return "v1", c.versionErr }
func (c *fakeCache) GetList(context.Context, string, string, app.Page) (app.VideoPage, bool, error) {
	return app.VideoPage{Total: 1}, c.hit, c.getErr
}
func (c *fakeCache) PutList(context.Context, string, string, app.VideoPage) error { return nil }
func (c *fakeCache) InvalidateList(context.Context, string) error                 { return nil }

func TestListCacheCountsEveryLookupOnce(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	fake := &fakeCache{}
	cache := prom.InstrumentListCache(reg, fake)
	// The lookups of app.Videos.List: a miss, a hit, and two errors.
	list := func() {
		if _, err := cache.ListVersion(context.Background(), "u"); err != nil {
			return
		}
		_, _, _ = cache.GetList(context.Background(), "u", "v1", app.Page{Number: 1, Size: 20})
	}
	list()
	fake.hit = true
	list()
	fake.getErr = errBoom
	list()
	fake.versionErr = errBoom
	list()

	got := values(t, reg, "videoproc_list_cache_requests_total")
	want := map[string]float64{"result=miss": 1, "result=hit": 1, "result=error": 2}
	if len(got) != len(want) {
		t.Errorf("lookups %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// The rest of the cache is passed through.
	if err := cache.InvalidateList(context.Background(), "u"); err != nil {
		t.Error(err)
	}
}

// TestLabelsAreBounded registers every collector of this package, as the
// services do, and checks each family's label names and values against
// fixed sets: no ids, e-mails, paths or free text can become a label.
func TestLabelsAreBounded(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	w := prom.NewWorker(reg)
	n := prom.NewNotifier(reg)
	o := prom.NewOutbox(reg, func(context.Context) (int64, error) { return 1, nil })
	cache := prom.InstrumentListCache(reg, &fakeCache{})
	for _, oc := range prom.JobOutcomes {
		w.RecordOutcome(oc, time.Second)
	}
	for _, oc := range prom.NotificationResults {
		n.RecordOutcome(oc, time.Second)
	}
	_ = n.InstrumentMailer(mailerFunc(func(context.Context, app.Mail) error { return nil })).Send(context.Background(), app.Mail{To: "ana@example.com"})
	o.Publisher(publisherFunc(func(context.Context, []app.Message) []error { return []error{nil} })).Publish(context.Background(), []app.Message{{ID: "m"}})
	_, _ = cache.ListVersion(context.Background(), "11111111-1111-4111-8111-111111111111")

	outcomes := []string{"done", "failed", "ignored", "retried", "dead_lettered", "requeued"}
	results := []string{"sent", "duplicate", "ignored", "retried", "dead_lettered", "requeued"}
	allowed := map[string]map[string][]string{
		"videoproc_jobs_total":                 {"outcome": outcomes},
		"videoproc_job_duration_seconds":       {"outcome": outcomes},
		"videoproc_jobs_in_progress":           {},
		"videoproc_frames_extracted_total":     {},
		"videoproc_notifications_total":        {"result": results},
		"videoproc_notifications_in_progress":  {},
		"videoproc_smtp_send_duration_seconds": {"result": {"ok", "error"}},
		"videoproc_outbox_published_total":     {"result": {"ok", "error"}},
		"videoproc_outbox_pending":             {},
		"videoproc_list_cache_requests_total":  {"result": {"hit", "miss", "error"}},
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		want, ok := allowed[f.GetName()]
		if !ok {
			t.Errorf("unexpected family %s: add it to this test (and docs/observability.md)", f.GetName())
			continue
		}
		seen[f.GetName()] = true
		for _, m := range f.GetMetric() {
			if len(m.GetLabel()) != len(want) {
				t.Errorf("%s{%s}: labels, want exactly %v", f.GetName(), labels(m), want)
			}
			for _, l := range m.GetLabel() {
				if vals, ok := want[l.GetName()]; !ok || !slices.Contains(vals, l.GetValue()) {
					t.Errorf("%s: label %s=%q is not in the allowed set %v", f.GetName(), l.GetName(), l.GetValue(), vals)
				}
			}
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("family %s not exported", name)
		}
	}
}
