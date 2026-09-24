package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
)

// HeaderAttempt is the message header holding the delivery attempt of a
// retried message (1-based; absent on the first attempt).
const HeaderAttempt = "attempt"

// maxBatch bounds the messages of one Publish call: every unroutable one
// must fit the buffer of returned messages (see Publisher.connect).
const maxBatch = 512

// Errors of Publish, per message.
var (
	// ErrNacked is wrapped when the broker refused a message (basic.nack).
	ErrNacked = errors.New("rabbitmq: message not accepted by the broker")
	// ErrUnroutable is wrapped when no queue was bound for the message
	// (basic.return of a mandatory publish).
	ErrUnroutable = errors.New("rabbitmq: message could not be routed to any queue")
)

// Publisher publishes persistent messages with publisher confirms: a
// message counts as published only once the broker has confirmed it, which
// for a durable queue means it has been written to disk. Messages are
// mandatory, so one no queue would receive is reported instead of dropped.
//
// It keeps one connection, opened on first use and reopened after any
// failure; on every (re)connection it declares its topology. It is safe
// for concurrent use; batches are published one at a time.
type Publisher struct {
	url      string
	name     string
	exchange string
	topology []WorkQueue
	log      *slog.Logger

	mu      sync.Mutex
	conn    *amqp.Connection
	ch      *amqp.Channel
	returns chan amqp.Return
}

// NewPublisher returns a publisher to exchange that declares topology on
// connect. name identifies the connection in the broker. It does not
// contact the broker.
func NewPublisher(url, name, exchange string, log *slog.Logger, topology ...WorkQueue) (*Publisher, error) {
	if err := validateURL(url); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Publisher{url: url, name: name, exchange: exchange, topology: topology, log: log}, nil
}

// Publish publishes msgs to the publisher's exchange, each with its Topic
// as routing key and its ID as message id, and returns one error per
// message, in order (nil: confirmed). At most maxBatch messages are sent at
// once; the rest fail and are retried by the caller.
func (p *Publisher) Publish(ctx context.Context, msgs []app.Message) []error {
	out := make([]outgoing, len(msgs))
	for i, m := range msgs {
		out[i] = outgoing{
			exchange: p.exchange,
			key:      m.Topic,
			msg: amqp.Publishing{
				ContentType: "application/json",
				MessageId:   m.ID,
				Body:        m.Body,
			},
		}
	}
	return p.publish(ctx, out)
}

// Retry republishes a delivery to the retry queue that delays its attempt
// number attempt (2 for the first retry) of work queue w, keeping its body,
// message id and headers.
func (p *Publisher) Retry(ctx context.Context, w WorkQueue, d amqp.Delivery, attempt int) error {
	if attempt < 2 || attempt > w.MaxAttempts() {
		return fmt.Errorf("rabbitmq: no retry queue for attempt %d of %s", attempt, w.Queue)
	}
	headers := amqp.Table{}
	for k, v := range d.Headers {
		if k == "x-death" { // bookkeeping of the broker's own dead-lettering
			continue
		}
		headers[k] = v
	}
	headers[HeaderAttempt] = int32(attempt) // #nosec G115 -- attempt <= MaxAttempts, a small number
	errs := p.publish(ctx, []outgoing{{
		exchange: "", // the default exchange routes by queue name
		key:      w.RetryQueue(attempt - 1),
		msg: amqp.Publishing{
			ContentType: d.ContentType,
			MessageId:   d.MessageId,
			Headers:     headers,
			Body:        d.Body,
		},
	}})
	return errs[0]
}

// outgoing is one message to publish.
type outgoing struct {
	exchange, key string
	msg           amqp.Publishing
}

func (p *Publisher) publish(ctx context.Context, batch []outgoing) []error {
	errs := make([]error, len(batch))
	fail := func(from int, err error) []error {
		for i := from; i < len(errs); i++ {
			if errs[i] == nil {
				errs[i] = err
			}
		}
		return errs
	}
	if len(batch) > maxBatch {
		fail(maxBatch, fmt.Errorf("rabbitmq: batch larger than %d messages", maxBatch))
		batch = batch[:maxBatch]
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.connect(ctx); err != nil {
		return fail(0, err)
	}

	confirms := make([]*amqp.DeferredConfirmation, len(batch))
	for i, o := range batch {
		msg := o.msg
		msg.DeliveryMode = amqp.Persistent
		msg.Timestamp = time.Now().UTC()
		dc, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, o.exchange, o.key, true, false, msg)
		if err != nil {
			p.reset()
			return fail(i, fmt.Errorf("rabbitmq: publish: %w", err))
		}
		confirms[i] = dc
	}
	for i, dc := range confirms {
		acked, err := dc.WaitContext(ctx)
		switch {
		case err != nil:
			// Timed out or the channel closed: the outcome is unknown, so
			// the message counts as not published (the caller publishes it
			// again; consumers tolerate duplicates).
			p.reset()
			return fail(i, fmt.Errorf("rabbitmq: waiting for the publisher confirm: %w", err))
		case !acked:
			errs[i] = ErrNacked
		}
	}

	// The broker sends basic.return before the confirm of an unroutable
	// mandatory message, and the client queues it in p.returns before
	// processing the confirm, so every return of this batch is here now.
	returned := false
drain:
	for {
		select {
		case r := <-p.returns:
			returned = true
			for i, o := range batch {
				if errs[i] == nil && o.msg.MessageId == r.MessageId && o.key == r.RoutingKey {
					errs[i] = fmt.Errorf("%w (exchange %q, key %q: %s)", ErrUnroutable, r.Exchange, r.RoutingKey, r.ReplyText)
				}
			}
		default:
			break drain
		}
	}
	if returned {
		// Someone removed a queue or binding: reconnecting declares the
		// topology again.
		p.reset()
	}
	return errs
}

// connect opens the connection and a confirm-mode channel if needed, and
// declares the topology.
func (p *Publisher) connect(ctx context.Context) error {
	if p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	p.reset()
	conn, err := dial(ctx, p.url, p.name)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err == nil {
		err = declare(ch, p.topology...)
	}
	if err == nil {
		err = ch.Confirm(false)
	}
	if err != nil {
		conn.Close()
		return fmt.Errorf("rabbitmq: publisher channel: %w", err)
	}
	p.conn, p.ch = conn, ch
	p.returns = ch.NotifyReturn(make(chan amqp.Return, maxBatch))
	p.log.InfoContext(ctx, "connected to the broker", slog.String("connection", p.name))
	return nil
}

// reset closes the connection; the next publish opens a new one.
func (p *Publisher) reset() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn, p.ch, p.returns = nil, nil, nil
}

// Close closes the connection.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reset()
	return nil
}

// attemptOf returns the attempt number of a delivery: its HeaderAttempt, or
// 1 when absent or invalid.
func attemptOf(headers amqp.Table) int {
	var n int64
	switch v := headers[HeaderAttempt].(type) {
	case int32:
		n = int64(v)
	case int64:
		n = v
	case int:
		n = int64(v)
	case int16:
		n = int64(v)
	case int8:
		n = int64(v)
	case string:
		n, _ = strconv.ParseInt(v, 10, 32)
	}
	if n < 1 {
		return 1
	}
	return int(n)
}
