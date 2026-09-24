package rabbitmq

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
)

// brokerURL returns RABBITMQ_TEST_URL (e.g. amqp://video:video@localhost:5672/
// with the compose stack up) or skips the test.
func brokerURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set")
	}
	return url
}

// testWorkQueue returns a work queue with unique names and short retry
// delays, and removes its resources when the test ends. It never touches
// the application's queues.
func testWorkQueue(t *testing.T, url string) WorkQueue {
	t.Helper()
	id := uuid.NewString()[:8]
	w := WorkQueue{
		Exchange:           "test-" + id,
		RoutingKey:         "test.job",
		Queue:              "test-" + id + ".work",
		DeadLetterExchange: "test-" + id + ".dlx",
		RetryDelays:        []time.Duration{50 * time.Millisecond, 50 * time.Millisecond},
	}
	t.Cleanup(func() {
		ch, closeCh := channel(t, url)
		defer closeCh()
		_, qs, _ := w.resources()
		for _, q := range qs {
			_, _ = ch.QueueDelete(q.Name, false, false, false)
		}
		_ = ch.ExchangeDelete(w.Exchange, false, false)
		_ = ch.ExchangeDelete(w.DeadLetterExchange, false, false)
	})
	return w
}

func channel(t *testing.T, url string) (*amqp.Channel, func()) {
	t.Helper()
	conn, err := dial(context.Background(), url, "test")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return ch, func() { conn.Close() }
}

// messageCount returns the number of ready messages in queue.
func messageCount(t *testing.T, url, queue string) int {
	t.Helper()
	ch, closeCh := channel(t, url)
	defer closeCh()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return q.Messages
}

// scriptedHandler fails each body a given number of times, then succeeds;
// "slow" blocks until its context ends or release is closed.
type scriptedHandler struct {
	mu       sync.Mutex
	failures map[string]int // body -> failures left (-1: always)
	attempts map[string]int
	done     map[string]bool
	gaveUp   map[string]bool
	started  chan string
	release  chan struct{}
}

func newScriptedHandler(failures map[string]int) *scriptedHandler {
	return &scriptedHandler{
		failures: failures, attempts: map[string]int{}, done: map[string]bool{}, gaveUp: map[string]bool{},
		started: make(chan string, 10), release: make(chan struct{}),
	}
}

func (h *scriptedHandler) Handle(ctx context.Context, body []byte) error {
	key := string(body)
	h.mu.Lock()
	h.attempts[key]++
	h.mu.Unlock()
	select {
	case h.started <- key:
	default:
	}
	switch key {
	case "malformed":
		return app.ErrMalformedMessage
	case "slow":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.release:
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if left := h.failures[key]; left != 0 {
		h.failures[key] = left - 1
		return errBoom
	}
	h.done[key] = true
	return nil
}

func (h *scriptedHandler) GiveUp(_ context.Context, body []byte, _ error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gaveUp[string(body)] = true
	return nil
}

func (h *scriptedHandler) snapshot() (attempts map[string]int, done, gaveUp map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, d, g := map[string]int{}, map[string]bool{}, map[string]bool{}
	for k, v := range h.attempts {
		a[k] = v
	}
	for k, v := range h.done {
		d[k] = v
	}
	for k, v := range h.gaveUp {
		g[k] = v
	}
	return a, d, g
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startConsumer(t *testing.T, url string, w WorkQueue, h Handler, pub *Publisher, shutdown time.Duration) (stop func()) {
	t.Helper()
	c, err := NewConsumer(ConsumerConfig{URL: url, Name: "test", Queue: w, Concurrency: 2, ShutdownTimeout: shutdown}, h, pub, nil)
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
			t.Fatal("consumer did not stop")
		}
	}
}

func publish(t *testing.T, pub *Publisher, w WorkQueue, bodies ...string) {
	t.Helper()
	msgs := make([]app.Message, len(bodies))
	for i, b := range bodies {
		msgs[i] = app.Message{ID: b, Topic: w.RoutingKey, Body: []byte(b)}
	}
	for i, err := range pub.Publish(context.Background(), msgs) {
		if err != nil {
			t.Fatalf("publish %s: %v", msgs[i].ID, err)
		}
	}
}

func TestBrokerRetriesAndDeadLetters(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	h := newScriptedHandler(map[string]int{"flaky": 2, "broken": -1})
	stop := startConsumer(t, url, w, h, pub, time.Second)
	defer stop()

	publish(t, pub, w, "ok", "flaky", "broken", "malformed")

	waitFor(t, "every message settled", 15*time.Second, func() bool {
		_, done, gaveUp := h.snapshot()
		return done["ok"] && done["flaky"] && gaveUp["broken"] && messageCount(t, url, w.DeadLetterQueue()) == 2
	})
	attempts, _, _ := h.snapshot()
	want := map[string]int{"ok": 1, "flaky": 3, "broken": 3, "malformed": 1}
	for k, n := range want {
		if attempts[k] != n {
			t.Errorf("%s handled %d times, want %d", k, attempts[k], n)
		}
	}
	for _, q := range []string{w.Queue, w.RetryQueue(1), w.RetryQueue(2)} {
		if n := messageCount(t, url, q); n != 0 {
			t.Errorf("%s holds %d messages, want 0", q, n)
		}
	}
}

func TestBrokerPublishUnroutable(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	errs := pub.Publish(context.Background(), []app.Message{
		{ID: "routed", Topic: w.RoutingKey, Body: []byte("{}")},
		{ID: "lost", Topic: "no.queue.bound", Body: []byte("{}")},
	})
	if errs[0] != nil || !errors.Is(errs[1], ErrUnroutable) {
		t.Fatalf("errs %v, want [nil, ErrUnroutable]", errs)
	}
	// The publisher reconnects and keeps working.
	if errs := pub.Publish(context.Background(), []app.Message{{ID: "again", Topic: w.RoutingKey, Body: []byte("{}")}}); errs[0] != nil {
		t.Fatal(errs[0])
	}
	if n := messageCount(t, url, w.Queue); n != 2 {
		t.Errorf("%d messages queued, want 2", n)
	}
}

func TestBrokerGracefulShutdownFinishesInFlight(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	h := newScriptedHandler(nil)
	stop := startConsumer(t, url, w, h, pub, 10*time.Second)
	publish(t, pub, w, "slow")
	<-h.started

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("the consumer stopped before its in-flight message finished")
	case <-time.After(300 * time.Millisecond):
	}
	close(h.release)
	<-stopped
	if _, done, _ := h.snapshot(); !done["slow"] {
		t.Error("the in-flight message was not finished")
	}
	if n := messageCount(t, url, w.Queue); n != 0 {
		t.Errorf("%d messages left, want 0 (acked)", n)
	}
}

func TestBrokerShutdownTimeoutRequeues(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	h := newScriptedHandler(nil)
	stop := startConsumer(t, url, w, h, pub, 200*time.Millisecond)
	publish(t, pub, w, "slow")
	<-h.started
	stop() // the handler never finishes: canceled after 200 ms, requeued

	if _, done, _ := h.snapshot(); done["slow"] {
		t.Error("the message finished")
	}
	waitFor(t, "the message requeued", 5*time.Second, func() bool { return messageCount(t, url, w.Queue) == 1 })
}
