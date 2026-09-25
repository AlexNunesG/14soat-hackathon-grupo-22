package rabbitmq

import (
	"context"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// scriptedDeadLetterHandler fails a body a given number of times, then
// succeeds, recording every call.
type scriptedDeadLetterHandler struct {
	mu       sync.Mutex
	failures map[string]int
	calls    map[string]int
	done     map[string]bool
}

func newScriptedDeadLetterHandler(failures map[string]int) *scriptedDeadLetterHandler {
	return &scriptedDeadLetterHandler{failures: failures, calls: map[string]int{}, done: map[string]bool{}}
}

func (h *scriptedDeadLetterHandler) Reconcile(_ context.Context, body []byte) error {
	key := string(body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[key]++
	if left := h.failures[key]; left != 0 {
		h.failures[key] = left - 1
		return errBoom
	}
	h.done[key] = true
	return nil
}

func (h *scriptedDeadLetterHandler) snapshot() (calls map[string]int, done map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, d := map[string]int{}, map[string]bool{}
	for k, v := range h.calls {
		c[k] = v
	}
	for k, v := range h.done {
		d[k] = v
	}
	return c, d
}

func startDeadLetterConsumer(t *testing.T, url, queue string, h DeadLetterHandler) (stop func()) {
	t.Helper()
	c, err := NewDeadLetterConsumer(ReconcilerConfig{URL: url, Name: "test-dlq", Queue: queue, ShutdownTimeout: time.Second}, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("dead-letter consumer did not stop")
		}
	}
}

// TestDeadLetterConsumerReconciles proves the ADR 0005 step 4 mechanism:
// once a job reaches a DLQ, however it got there (the application's own
// give-up, or RabbitMQ's own x-delivery-limit), DeadLetterConsumer
// reconciles it (here, eventually succeeds after one transient failure),
// and a reconcile failure is retried in place (nack with requeue) rather
// than dropped.
func TestDeadLetterConsumerReconciles(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	// A message already in the DLQ, as if the work queue's own
	// x-delivery-limit (or the application's GiveUp) had dead-lettered it.
	declareTestQueues(t, url, w)
	publishDirect(t, url, w.DeadLetterQueue(), "flaky", "ok")

	h := newScriptedDeadLetterHandler(map[string]int{"flaky": 1})
	stop := startDeadLetterConsumer(t, url, w.DeadLetterQueue(), h)
	defer stop()

	waitFor(t, "every dead letter reconciled", 10*time.Second, func() bool {
		_, done := h.snapshot()
		return done["flaky"] && done["ok"]
	})
	calls, _ := h.snapshot()
	if calls["flaky"] < 2 {
		t.Errorf("flaky reconciled %d times, want at least 2 (one failure then a retry)", calls["flaky"])
	}
	if n := messageCount(t, url, w.DeadLetterQueue()); n != 0 {
		t.Errorf("%d messages left in the DLQ, want 0 (all reconciled)", n)
	}
}

// declareTestQueues declares w's resources without going through a
// consumer or publisher, so the DLQ exists before the reconciler consumes
// it (as it always does in production: the work queue's own topology
// declares it on every relay/consumer connect).
func declareTestQueues(t *testing.T, url string, w WorkQueue) {
	t.Helper()
	ch, closeCh := channel(t, url)
	defer closeCh()
	if err := declare(ch, w); err != nil {
		t.Fatal(err)
	}
}

// publishDirect publishes bodies straight to queue via the default
// exchange, bypassing the topic exchange and its retry/DLX machinery: it
// simulates a message that is already sitting in a DLQ.
func publishDirect(t *testing.T, url, queue string, bodies ...string) {
	t.Helper()
	ch, closeCh := channel(t, url)
	defer closeCh()
	for _, b := range bodies {
		err := ch.PublishWithContext(context.Background(), "", queue, true, false, amqp.Publishing{
			DeliveryMode: amqp.Persistent, Body: []byte(b),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
