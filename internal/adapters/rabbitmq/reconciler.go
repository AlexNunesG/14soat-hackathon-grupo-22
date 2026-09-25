package rabbitmq

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// DeadLetterHandler reconciles one message that reached a dead-letter
// queue by RabbitMQ's own doing (a quorum queue's x-delivery-limit,
// ADR 0005) rather than the application's own give-up-after-N-attempts
// logic: the consumer never got a chance to run its ordinary Handler at
// all, so nothing has recorded the job's outcome yet. Reconcile is called
// again for a redelivered message, so it must be idempotent.
type DeadLetterHandler interface {
	Reconcile(ctx context.Context, body []byte) error
}

// ReconcilerConfig configures a DeadLetterConsumer.
type ReconcilerConfig struct {
	// URL is the AMQP URL of the broker.
	URL string
	// Name identifies the consumer's connections and consumer tag.
	Name string
	// Queue is the dead-letter queue to consume, e.g.
	// rabbitmq.VideoProcess.DeadLetterQueue(). It must already exist: a
	// work queue's own topology (declared by every relay and consumer)
	// declares it, so the reconciler only ever consumes it, never
	// declares it itself.
	Queue string
	// ShutdownTimeout bounds how long the in-flight message may take to
	// settle after Run's context is done, like Consumer.
	ShutdownTimeout time.Duration
}

// DeadLetterConsumer consumes a dead-letter queue with manual acks and a
// prefetch of 1 (docs/adr/0005-quorum-queues-delivery-limit.md,
// "Reconciling the DLQ"). Unlike Consumer, there is no further retry queue
// to republish into — the message already reached its terminal DLQ — so a
// failure to reconcile (e.g. the database is down) is nacked with requeue
// after a short delay and tried again in place, instead of being dropped
// or looping tightly. A message DeadLetterHandler.Reconcile settles
// (returns nil for) is acked.
type DeadLetterConsumer struct {
	cfg     ReconcilerConfig
	handler DeadLetterHandler
	log     *slog.Logger
}

// NewDeadLetterConsumer returns a dead-letter consumer.
func NewDeadLetterConsumer(cfg ReconcilerConfig, handler DeadLetterHandler, log *slog.Logger) (*DeadLetterConsumer, error) {
	if err := validateURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.Queue == "" {
		return nil, fmt.Errorf("rabbitmq: dead-letter consumer needs a queue")
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &DeadLetterConsumer{cfg: cfg, handler: handler, log: log.With(slog.String("queue", cfg.Queue))}, nil
}

// Run consumes until ctx is done, then cancels the consumer, requeues
// whatever was buffered and returns nil. It reconnects with backoff when
// the connection drops, like Consumer.
func (c *DeadLetterConsumer) Run(ctx context.Context) error {
	delay := minReconnectDelay
	for ctx.Err() == nil {
		s, err := c.open(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.log.WarnContext(ctx, "cannot consume dead letters, retrying", slog.Duration("in", delay), slog.Any("error", err))
			sleep(ctx, delay)
			delay = min(2*delay, maxReconnectDelay)
			continue
		}
		delay = minReconnectDelay
		c.log.InfoContext(ctx, "reconciling dead letters")
		graceful := c.consume(ctx, s)
		s.close()
		if !graceful {
			c.log.WarnContext(ctx, "connection to the broker lost while reconciling dead letters; reconnecting")
			continue
		}
		return nil
	}
	return nil
}

// open connects and starts consuming the (already declared) dead-letter
// queue with a prefetch of 1: messages here are rare and worth handling
// one at a time.
func (c *DeadLetterConsumer) open(ctx context.Context) (*session, error) {
	dctx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	conn, err := dial(dctx, c.cfg.URL, c.cfg.Name)
	if err != nil {
		return nil, err
	}
	s := &session{conn: conn, tag: consumerTag(c.cfg.Name)}
	s.ch, err = conn.Channel()
	if err == nil {
		// Passive: the queue is declared by the work queue's own topology,
		// never by the reconciler, so a queue that does not exist yet (a
		// connection racing the first declare) is reported instead of
		// silently created with the wrong arguments.
		_, err = s.ch.QueueDeclarePassive(c.cfg.Queue, true, false, false, false, nil)
	}
	if err == nil {
		err = s.ch.Qos(1, 0, false)
	}
	if err == nil {
		s.deliveries, err = s.ch.Consume(c.cfg.Queue, s.tag, false, false, false, false, nil)
	}
	if err != nil {
		s.close()
		return nil, fmt.Errorf("rabbitmq: consume dead letters %s: %w", c.cfg.Queue, err)
	}
	return s, nil
}

// consume hands deliveries to the handler until ctx is done (cancels the
// consumer, requeues what was buffered, returns true) or the connection is
// lost (returns false).
func (c *DeadLetterConsumer) consume(ctx context.Context, s *session) bool {
	for {
		select {
		case <-ctx.Done():
			if err := s.ch.Cancel(s.tag, false); err != nil {
				c.log.WarnContext(ctx, "cancel dead-letter consumer", slog.Any("error", err))
			}
			for d := range s.deliveries {
				requeue(d)
			}
			return true
		case d, ok := <-s.deliveries:
			if !ok {
				return false
			}
			c.handle(ctx, d)
		}
	}
}

// handle reconciles one delivery and settles it.
func (c *DeadLetterConsumer) handle(ctx context.Context, d amqp.Delivery) {
	err := c.handler.Reconcile(ctx, d.Body)
	if err == nil {
		if aerr := d.Ack(false); aerr != nil {
			c.log.WarnContext(ctx, "could not ack a reconciled dead letter; the broker will redeliver it", slog.Any("error", aerr))
		}
		return
	}
	c.log.ErrorContext(ctx, "could not reconcile a dead letter; retrying", slog.Any("error", err))
	sleep(ctx, requeueDelay)
	if nerr := d.Nack(false, true); nerr != nil {
		c.log.WarnContext(ctx, "could not requeue a dead letter after a reconcile failure", slog.Any("error", nerr))
	}
}
