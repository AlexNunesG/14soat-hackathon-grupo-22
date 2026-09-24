package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
)

// fakeAck records how a delivery was settled.
type fakeAck struct {
	mu      sync.Mutex
	settled []string
	err     error
}

func (a *fakeAck) record(s string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settled = append(a.settled, s)
	return a.err
}

func (a *fakeAck) Ack(uint64, bool) error { return a.record("ack") }

func (a *fakeAck) Nack(_ uint64, _ bool, requeue bool) error {
	if requeue {
		return a.record("requeue")
	}
	return a.record("dead-letter")
}

func (a *fakeAck) Reject(_ uint64, requeue bool) error { return a.Nack(0, false, requeue) }

func (a *fakeAck) result() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fmt.Sprint(a.settled)
}

// fakeHandler returns handleErr from Handle and giveUpErr from GiveUp,
// recording the calls.
type fakeHandler struct {
	handleErr error
	giveUpErr error
	// cancel, when set, is called inside Handle (a shutdown mid-message).
	cancel  context.CancelFunc
	handled int
	gaveUp  []error
}

func (h *fakeHandler) Handle(context.Context, []byte) error {
	h.handled++
	if h.cancel != nil {
		h.cancel()
	}
	return h.handleErr
}

func (h *fakeHandler) GiveUp(_ context.Context, _ []byte, cause error) error {
	h.gaveUp = append(h.gaveUp, cause)
	return h.giveUpErr
}

// fakeRetrier records retries.
type fakeRetrier struct {
	err      error
	attempts []int
	queue    string
}

func (r *fakeRetrier) Retry(_ context.Context, w WorkQueue, _ amqp.Delivery, attempt int) error {
	r.attempts = append(r.attempts, attempt)
	r.queue = w.RetryQueue(attempt - 1)
	return r.err
}

var testQueue = WorkQueue{
	Exchange: "ex", RoutingKey: "k", Queue: "q", DeadLetterExchange: "dlx",
	RetryDelays: []time.Duration{time.Millisecond, time.Millisecond},
}

func newTestConsumer(t *testing.T, h Handler, r retrier, maxAttempts int) *Consumer {
	t.Helper()
	c, err := newConsumer(ConsumerConfig{
		URL: "amqp://localhost:5672/", Name: "test", Queue: testQueue,
		Concurrency: 2, MaxAttempts: maxAttempts,
	}, h, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func delivery(ack amqp.Acknowledger, attempt int) amqp.Delivery {
	d := amqp.Delivery{Acknowledger: ack, MessageId: "m-1", Body: []byte(`{"video_id":"v"}`)}
	if attempt > 0 {
		d.Headers = amqp.Table{HeaderAttempt: int32(attempt)} // #nosec G115 -- small test values
	}
	return d
}

func TestHandleSettlesDeliveries(t *testing.T) {
	malformed := fmt.Errorf("%w: bad json", app.ErrMalformedMessage)
	tests := []struct {
		name        string
		attempt     int // 0: no header
		handleErr   error
		giveUpErr   error
		retryErr    error
		shutdown    bool
		want        string
		wantRetries []int
		wantGiveUp  bool
	}{
		{name: "success", want: "[ack]"},
		{name: "first failure is retried", handleErr: errBoom, want: "[ack]", wantRetries: []int{2}},
		{name: "second failure is retried", attempt: 2, handleErr: errBoom, want: "[ack]", wantRetries: []int{3}},
		{name: "last attempt gives up", attempt: 3, handleErr: errBoom, want: "[dead-letter]", wantGiveUp: true},
		{name: "beyond the last attempt gives up", attempt: 9, handleErr: errBoom, want: "[dead-letter]", wantGiveUp: true},
		{name: "malformed is dead-lettered at once", handleErr: malformed, want: "[dead-letter]"},
		{name: "shutdown requeues", handleErr: context.Canceled, shutdown: true, want: "[requeue]"},
		{name: "retry publish failure requeues", handleErr: errBoom, retryErr: errBoom, want: "[requeue]", wantRetries: []int{2}},
		{name: "give up failure requeues", attempt: 3, handleErr: errBoom, giveUpErr: errBoom, want: "[requeue]", wantGiveUp: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := &fakeHandler{handleErr: tt.handleErr, giveUpErr: tt.giveUpErr}
			if tt.shutdown {
				h.cancel = cancel
			}
			r := &fakeRetrier{err: tt.retryErr}
			c := newTestConsumer(t, h, r, 0)
			ack := &fakeAck{}
			if tt.retryErr != nil || tt.giveUpErr != nil {
				// The requeue waits requeueDelay unless the context ends.
				go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			}

			c.handle(ctx, delivery(ack, tt.attempt))

			if got := ack.result(); got != tt.want {
				t.Errorf("settled %s, want %s", got, tt.want)
			}
			if fmt.Sprint(r.attempts) != fmt.Sprint(tt.wantRetries) && (len(r.attempts) > 0 || len(tt.wantRetries) > 0) {
				t.Errorf("retries %v, want %v", r.attempts, tt.wantRetries)
			}
			if gave := len(h.gaveUp) > 0; gave != tt.wantGiveUp {
				t.Errorf("gave up %v, want %v", gave, tt.wantGiveUp)
			}
			if tt.wantGiveUp && !errors.Is(h.gaveUp[0], errBoom) {
				t.Errorf("GiveUp cause %v, want the handler's error", h.gaveUp[0])
			}
		})
	}
}

func TestHandleRetryGoesToTheQueueOfTheAttempt(t *testing.T) {
	r := &fakeRetrier{}
	c := newTestConsumer(t, &fakeHandler{handleErr: errBoom}, r, 0)
	c.handle(context.Background(), delivery(&fakeAck{}, 2))
	if r.queue != "q.retry.2" {
		t.Errorf("retry queue %q, want q.retry.2", r.queue)
	}
}

func TestHandleHonorsLowerMaxAttempts(t *testing.T) {
	h := &fakeHandler{handleErr: errBoom}
	c := newTestConsumer(t, h, &fakeRetrier{}, 1) // no retries at all
	ack := &fakeAck{}
	c.handle(context.Background(), delivery(ack, 0))
	if ack.result() != "[dead-letter]" || len(h.gaveUp) != 1 {
		t.Errorf("settled %s, gave up %d times", ack.result(), len(h.gaveUp))
	}
}

func TestHandleAckFailureIsOnlyLogged(t *testing.T) {
	c := newTestConsumer(t, &fakeHandler{}, &fakeRetrier{}, 0)
	ack := &fakeAck{err: amqp.ErrClosed}
	c.handle(context.Background(), delivery(ack, 0)) // must not panic or block
	if ack.result() != "[ack]" {
		t.Errorf("settled %s", ack.result())
	}
}

func TestNewConsumerValidates(t *testing.T) {
	base := ConsumerConfig{URL: "amqp://localhost/", Queue: testQueue, Concurrency: 1}
	for name, edit := range map[string]func(*ConsumerConfig){
		"bad URL":              func(c *ConsumerConfig) { c.URL = "http://x" },
		"zero concurrency":     func(c *ConsumerConfig) { c.Concurrency = 0 },
		"too many attempts":    func(c *ConsumerConfig) { c.MaxAttempts = testQueue.MaxAttempts() + 1 },
		"negative max attempt": func(c *ConsumerConfig) { c.MaxAttempts = -1 },
	} {
		cfg := base
		edit(&cfg)
		if _, err := newConsumer(cfg, &fakeHandler{}, &fakeRetrier{}, nil); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	c, err := newConsumer(base, &fakeHandler{}, &fakeRetrier{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.cfg.MaxAttempts != testQueue.MaxAttempts() || c.cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("defaults: %+v", c.cfg)
	}
}

func TestAttemptOf(t *testing.T) {
	tests := []struct {
		headers amqp.Table
		want    int
	}{
		{nil, 1},
		{amqp.Table{}, 1},
		{amqp.Table{HeaderAttempt: int32(3)}, 3},
		{amqp.Table{HeaderAttempt: int64(2)}, 2},
		{amqp.Table{HeaderAttempt: int8(4)}, 4},
		{amqp.Table{HeaderAttempt: "2"}, 2},
		{amqp.Table{HeaderAttempt: "x"}, 1},
		{amqp.Table{HeaderAttempt: int32(0)}, 1},
		{amqp.Table{HeaderAttempt: int32(-5)}, 1},
		{amqp.Table{HeaderAttempt: 2.5}, 1},
	}
	for _, tt := range tests {
		if got := attemptOf(tt.headers); got != tt.want {
			t.Errorf("attemptOf(%v) = %d, want %d", tt.headers, got, tt.want)
		}
	}
}

var errBoom = errors.New("boom")
