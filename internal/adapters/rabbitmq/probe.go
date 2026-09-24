// Package rabbitmq holds the RabbitMQ adapters: the topology (exchanges,
// work queues with retry and dead-letter queues, declared as code and
// exported as deploy/rabbitmq/definitions.json), the publisher (publisher
// confirms, persistent messages), the consumer (manual acks, prefetch =
// concurrency, retries with backoff, graceful shutdown, reconnection) and
// the readiness probe.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Probe checks that the broker accepts connections: it dials, opens a
// channel and closes both. It keeps no connection between calls.
type Probe struct {
	url string
}

// NewProbe returns a Probe for the AMQP URL (amqp:// or amqps://). It fails
// when the URL cannot be parsed; it does not contact the broker.
func NewProbe(url string) (*Probe, error) {
	if err := validateURL(url); err != nil {
		return nil, err
	}
	return &Probe{url: url}, nil
}

// Ping dials the broker and opens a channel, within ctx's deadline (or 5 s
// when it has none).
func (p *Probe) Ping(ctx context.Context) (err error) {
	conn, err := dial(ctx, p.url, "readiness-probe")
	if err != nil {
		return err
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, amqp.ErrClosed) && err == nil {
			err = fmt.Errorf("rabbitmq: close: %w", cerr)
		}
	}()
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("rabbitmq: open channel: %w", err)
	}
	if err := ch.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("rabbitmq: close channel: %w", err)
	}
	return nil
}
