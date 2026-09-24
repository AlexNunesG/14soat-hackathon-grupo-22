package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
	"video-processor/internal/platform/logging"
)

// Defaults of the consumer.
const (
	// DefaultShutdownTimeout is how long in-flight jobs may run after Run's
	// context is done, when ConsumerConfig leaves it zero.
	DefaultShutdownTimeout = 15 * time.Second
	minReconnectDelay      = 500 * time.Millisecond
	maxReconnectDelay      = 5 * time.Second
	// requeueDelay slows down a message that is requeued because recording
	// its outcome failed (e.g. the database is down), so it does not spin.
	requeueDelay = 2 * time.Second
	// abortWait bounds the wait for jobs to stop once they are canceled.
	abortWait = 10 * time.Second
)

// Handler processes the messages of a work queue.
type Handler interface {
	// Handle processes one message body. It returns nil when the message
	// is done with, an error wrapping app.ErrMalformedMessage when the body
	// can never be processed, one wrapping app.ErrPermanent when retrying
	// cannot help, and any other error for a failure worth retrying. ctx
	// is canceled when the consumer stops before the handler is done; the
	// message is then requeued.
	Handle(ctx context.Context, body []byte) error
	// GiveUp is called with the last error when a message failed on its
	// last attempt, before it is dead-lettered.
	GiveUp(ctx context.Context, body []byte, cause error) error
}

// retrier republishes a failed delivery for a later attempt (Publisher).
type retrier interface {
	Retry(ctx context.Context, w WorkQueue, d amqp.Delivery, attempt int) error
}

// ConsumerConfig configures a Consumer.
type ConsumerConfig struct {
	// URL is the AMQP URL of the broker.
	URL string
	// Name identifies the consumer's connections and consumer tags.
	Name string
	// Queue is the work queue consumed; its topology is declared on every
	// connection.
	Queue WorkQueue
	// Concurrency is the number of messages handled at the same time; it is
	// also the prefetch count, so the broker never hands this consumer more
	// messages than it can work on.
	Concurrency int
	// MaxAttempts is how many times a message is tried before it is given
	// up on (at most Queue.MaxAttempts(); 0 means that).
	MaxAttempts int
	// ShutdownTimeout is how long in-flight messages may take to finish
	// after Run's context is done; then they are canceled and requeued.
	ShutdownTimeout time.Duration
}

// Consumer consumes a work queue with manual acknowledgements:
//
//   - success: ack;
//   - retryable failure on attempt n < MaxAttempts: republish to the retry
//     queue n (delayed by the queue's TTL, with header attempt = n+1), then
//     ack;
//   - failure on the last attempt: Handler.GiveUp, then nack without
//     requeue, which dead-letters the message to the DLQ;
//   - malformed message, or a failure wrapping app.ErrPermanent: nack
//     without requeue (DLQ) at once;
//   - interrupted by shutdown: nack with requeue.
//
// A message is only acked after its outcome is recorded, so a crash at any
// point makes the broker redeliver it (at-least-once; handlers are
// idempotent). Run reconnects with backoff when the connection drops.
type Consumer struct {
	cfg     ConsumerConfig
	handler Handler
	retrier retrier
	log     *slog.Logger
}

// NewConsumer returns a consumer; retries are republished with publisher.
func NewConsumer(cfg ConsumerConfig, handler Handler, publisher *Publisher, log *slog.Logger) (*Consumer, error) {
	return newConsumer(cfg, handler, publisher, log)
}

func newConsumer(cfg ConsumerConfig, handler Handler, r retrier, log *slog.Logger) (*Consumer, error) {
	if err := validateURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.Concurrency < 1 {
		return nil, fmt.Errorf("rabbitmq: consumer concurrency %d, want at least 1", cfg.Concurrency)
	}
	if limit := cfg.Queue.MaxAttempts(); cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = limit
	} else if cfg.MaxAttempts < 1 || cfg.MaxAttempts > limit {
		return nil, fmt.Errorf("rabbitmq: max attempts %d, want 1 to %d (one more than the retry delays of %s)",
			cfg.MaxAttempts, limit, cfg.Queue.Queue)
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Consumer{cfg: cfg, handler: handler, retrier: r, log: log.With(slog.String("queue", cfg.Queue.Queue))}, nil
}

// Run consumes until ctx is done, then shuts down gracefully: it stops
// taking messages, requeues the ones received but not started, and waits
// up to ShutdownTimeout for the in-flight ones, which are then canceled
// (and requeued). It returns nil after a graceful shutdown.
func (c *Consumer) Run(ctx context.Context) error {
	// Jobs outlive ctx until the shutdown timeout.
	jobs, abort := context.WithCancel(context.WithoutCancel(ctx))
	defer abort()
	var (
		inflight sync.WaitGroup
		slots    = make(chan struct{}, c.cfg.Concurrency)
		delay    = minReconnectDelay
	)
	for ctx.Err() == nil {
		s, err := c.open(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.log.WarnContext(ctx, "cannot consume, retrying", slog.Duration("in", delay), slog.Any("error", err))
			sleep(ctx, delay)
			delay = min(2*delay, maxReconnectDelay)
			continue
		}
		delay = minReconnectDelay
		c.log.InfoContext(ctx, "consuming", slog.Int("concurrency", c.cfg.Concurrency), slog.Int("max_attempts", c.cfg.MaxAttempts))
		c.consume(ctx, jobs, s, slots, &inflight)
		if ctx.Err() == nil {
			c.log.WarnContext(ctx, "connection to the broker lost; reconnecting")
			// Jobs of the lost connection keep running; their acks fail and
			// the broker redelivers those messages.
			s.close()
			continue
		}
		c.drain(jobs, abort, &inflight)
		s.close()
		return nil
	}
	c.drain(jobs, abort, &inflight)
	return nil
}

// drain waits for the in-flight jobs, canceling them after the shutdown
// timeout.
func (c *Consumer) drain(ctx context.Context, abort context.CancelFunc, inflight *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		c.log.InfoContext(ctx, "consumer stopped; in-flight messages finished")
		return
	case <-time.After(c.cfg.ShutdownTimeout):
	}
	c.log.WarnContext(ctx, "shutdown timeout: canceling in-flight messages", slog.Duration("timeout", c.cfg.ShutdownTimeout))
	abort()
	select {
	case <-done:
	case <-time.After(abortWait):
		c.log.ErrorContext(ctx, "in-flight messages did not stop; the broker requeues them when the connection closes")
	}
}

// session is one connection with its consuming channel.
type session struct {
	conn       *amqp.Connection
	ch         *amqp.Channel
	tag        string
	deliveries <-chan amqp.Delivery
}

func (s *session) close() {
	_ = s.conn.Close()
}

// open connects, declares the topology, sets the prefetch and starts
// consuming.
func (c *Consumer) open(ctx context.Context) (*session, error) {
	dctx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	conn, err := dial(dctx, c.cfg.URL, c.cfg.Name)
	if err != nil {
		return nil, err
	}
	s := &session{conn: conn, tag: consumerTag(c.cfg.Name)}
	s.ch, err = conn.Channel()
	if err == nil {
		err = declare(s.ch, c.cfg.Queue)
	}
	if err == nil {
		err = s.ch.Qos(c.cfg.Concurrency, 0, false)
	}
	if err == nil {
		s.deliveries, err = s.ch.Consume(c.cfg.Queue.Queue, s.tag, false, false, false, false, nil)
	}
	if err != nil {
		s.close()
		return nil, fmt.Errorf("rabbitmq: consume %s: %w", c.cfg.Queue.Queue, err)
	}
	return s, nil
}

// consume hands deliveries to handlers until ctx is done (then it cancels
// the consumer and requeues what was received but not started) or the
// connection is lost.
func (c *Consumer) consume(ctx, jobs context.Context, s *session, slots chan struct{}, inflight *sync.WaitGroup) {
	for {
		select {
		case <-ctx.Done():
			c.stop(s)
			return
		case d, ok := <-s.deliveries:
			if !ok {
				return
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				requeue(d)
				c.stop(s)
				return
			}
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				defer func() { <-slots }()
				c.handle(jobs, d)
			}()
		}
	}
}

// stop cancels the consumer and requeues the deliveries still buffered.
func (c *Consumer) stop(s *session) {
	if err := s.ch.Cancel(s.tag, false); err != nil {
		c.log.Warn("cancel consumer", slog.Any("error", err))
	}
	// After basic.cancel-ok the client flushes what it buffered and closes
	// the deliveries channel (it is also closed if the connection drops).
	for d := range s.deliveries {
		requeue(d)
	}
}

func requeue(d amqp.Delivery) { _ = d.Nack(false, true) }

// handle processes one delivery and settles it (see Consumer). The
// handler's context carries the delivery's correlation fields
// (deliveryContext), so every log line about the message has them.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	attempt := attemptOf(d.Headers)
	ctx = deliveryContext(ctx, d, attempt)
	err := c.handler.Handle(ctx, d.Body)
	switch {
	case err == nil:
		c.settle(ctx, "ack", d.Ack(false))

	case ctx.Err() != nil:
		c.log.WarnContext(ctx, "message interrupted by shutdown; requeued", slog.Any("error", err))
		c.settle(ctx, "requeue", d.Nack(false, true))

	case errors.Is(err, app.ErrMalformedMessage):
		c.log.ErrorContext(ctx, "malformed message dead-lettered", slog.Any("error", err))
		c.settle(ctx, "dead-letter", d.Nack(false, false))

	case errors.Is(err, app.ErrPermanent):
		c.log.ErrorContext(ctx, "message failed permanently; dead-lettered without retry", slog.Any("error", err))
		c.settle(ctx, "dead-letter", d.Nack(false, false))

	case attempt < c.cfg.MaxAttempts:
		c.log.WarnContext(ctx, "message failed; retrying later",
			slog.Duration("in", c.cfg.Queue.RetryDelays[attempt-1]), slog.Any("error", err))
		if rerr := c.retrier.Retry(ctx, c.cfg.Queue, d, attempt+1); rerr != nil {
			c.log.ErrorContext(ctx, "could not schedule the retry; requeued", slog.Any("error", rerr))
			sleep(ctx, requeueDelay)
			c.settle(ctx, "requeue", d.Nack(false, true))
			return
		}
		c.settle(ctx, "ack", d.Ack(false))

	default:
		c.log.ErrorContext(ctx, "message failed on its last attempt; giving up", slog.Any("error", err))
		if gerr := c.handler.GiveUp(ctx, d.Body, err); gerr != nil {
			c.log.ErrorContext(ctx, "could not record the failure; requeued", slog.Any("error", gerr))
			sleep(ctx, requeueDelay)
			c.settle(ctx, "requeue", d.Nack(false, true))
			return
		}
		c.settle(ctx, "dead-letter", d.Nack(false, false))
	}
}

// settle logs a failed ack/nack: the channel is gone, and the broker
// redelivers the message to another consumer.
func (c *Consumer) settle(ctx context.Context, what string, err error) {
	if err != nil {
		c.log.WarnContext(ctx, "could not "+what+" the message; the broker will redeliver it", slog.Any("error", err))
	}
}

// bodyIDs are the ids a message body may carry: video_id in every message
// of the videos exchange, event_id in video events.
type bodyIDs struct {
	VideoID string `json:"video_id"`
	EventID string `json:"event_id"`
}

// deliveryContext returns ctx with the correlation fields of d
// (docs/observability.md): its correlation id as request_id (when it is a
// valid one), message_id, attempt, and the video_id and event_id of its
// body when they are UUIDs. A malformed body only adds nothing.
func deliveryContext(ctx context.Context, d amqp.Delivery, attempt int) context.Context {
	attrs := []slog.Attr{
		slog.String(logging.KeyMessageID, d.MessageId),
		slog.Int(logging.KeyAttempt, attempt),
	}
	if logging.ValidRequestID(d.CorrelationId) {
		attrs = append([]slog.Attr{slog.String(logging.KeyRequestID, d.CorrelationId)}, attrs...)
	}
	var ids bodyIDs
	if json.Unmarshal(d.Body, &ids) == nil {
		if uuid.Validate(ids.VideoID) == nil {
			attrs = append(attrs, slog.String(logging.KeyVideoID, ids.VideoID))
		}
		if uuid.Validate(ids.EventID) == nil {
			attrs = append(attrs, slog.String(logging.KeyEventID, ids.EventID))
		}
	}
	return logging.WithAttrs(ctx, attrs...)
}

// consumerTag is unique per consumer: name, host and a random suffix.
func consumerTag(name string) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%s-%s", name, host, uuid.NewString()[:8])
}
