package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"video-processor/internal/app"
)

// fakeOutbox is an in-memory app.OutboxStore: failed messages stay queued.
type fakeOutbox struct {
	mu     sync.Mutex
	queue  []app.Message
	passes int
	err    error
}

func (o *fakeOutbox) add(msgs ...app.Message) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queue = append(o.queue, msgs...)
}

func (o *fakeOutbox) pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.queue)
}

func (o *fakeOutbox) Relay(ctx context.Context, limit int, publish app.PublishFunc) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.passes++
	if o.err != nil {
		return 0, o.err
	}
	batch := o.queue[:min(limit, len(o.queue))]
	errs := publish(ctx, batch)
	var kept []app.Message
	for i, err := range errs {
		if err != nil {
			kept = append(kept, batch[i])
		}
	}
	o.queue = append(kept, o.queue[len(batch):]...)
	return len(batch), nil
}

// fakePublisher records what it publishes; fail makes every publish fail.
type fakePublisher struct {
	mu        sync.Mutex
	published []app.Message
	fail      bool
}

func (p *fakePublisher) Publish(_ context.Context, msgs []app.Message) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		if p.fail {
			errs[i] = errBoom
			continue
		}
		p.published = append(p.published, m)
	}
	return errs
}

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

func (p *fakePublisher) setFail(fail bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = fail
}

func messages(n int) []app.Message {
	msgs := make([]app.Message, n)
	for i := range msgs {
		msgs[i] = app.Message{ID: string(rune('a' + i)), Topic: app.TopicVideoUploaded, Body: []byte("{}")}
	}
	return msgs
}

// eventually polls cond for up to 2 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runRelay(t *testing.T, relay *app.OutboxRelay) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Run(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("relay did not stop")
		}
	}
}

func TestRelayPublishesOnStartAndInFullBatches(t *testing.T) {
	outbox, pub := &fakeOutbox{}, &fakePublisher{}
	outbox.add(messages(7)...)
	stop := runRelay(t, app.NewOutboxRelay(outbox, pub, nil, time.Hour, 3))
	defer stop()
	// The first pass runs at once and repeats while batches are full:
	// 3 + 3 + 1 messages, without waiting for the (1 h) interval.
	eventually(t, "every message published", func() bool { return pub.count() == 7 })
	if outbox.pending() != 0 {
		t.Errorf("%d messages left", outbox.pending())
	}
}

func TestRelayNotifyWakesItUp(t *testing.T) {
	outbox, pub := &fakeOutbox{}, &fakePublisher{}
	relay := app.NewOutboxRelay(outbox, pub, nil, time.Hour, 10)
	stop := runRelay(t, relay)
	defer stop()
	eventually(t, "the first pass", func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return outbox.passes >= 1
	})
	outbox.add(messages(2)...)
	relay.Notify()
	relay.Notify() // never blocks
	eventually(t, "the notified pass", func() bool { return pub.count() == 2 })
}

func TestRelayRetriesFailedPublishesOnTheInterval(t *testing.T) {
	outbox, pub := &fakeOutbox{}, &fakePublisher{fail: true}
	outbox.add(messages(2)...)
	stop := runRelay(t, app.NewOutboxRelay(outbox, pub, nil, 10*time.Millisecond, 10))
	defer stop()
	eventually(t, "a few failed passes", func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return outbox.passes >= 3
	})
	if outbox.pending() != 2 {
		t.Fatalf("%d messages pending, want 2 kept after failures", outbox.pending())
	}
	pub.setFail(false) // the broker is back
	eventually(t, "the messages published", func() bool { return pub.count() == 2 && outbox.pending() == 0 })
}

func TestRelayFlushesWhenStopping(t *testing.T) {
	outbox, pub := &fakeOutbox{}, &fakePublisher{}
	relay := app.NewOutboxRelay(outbox, pub, nil, time.Hour, 10)
	stop := runRelay(t, relay)
	eventually(t, "the first pass", func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return outbox.passes >= 1
	})
	outbox.add(messages(3)...) // queued by the last requests, no Notify
	stop()
	if pub.count() != 3 {
		t.Errorf("published %d, want 3 on the final pass", pub.count())
	}
}

func TestRelaySurvivesStoreErrors(t *testing.T) {
	outbox, pub := &fakeOutbox{err: errBoom}, &fakePublisher{}
	stop := runRelay(t, app.NewOutboxRelay(outbox, pub, nil, 5*time.Millisecond, 10))
	eventually(t, "several passes despite errors", func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return outbox.passes >= 3
	})
	stop()
}
