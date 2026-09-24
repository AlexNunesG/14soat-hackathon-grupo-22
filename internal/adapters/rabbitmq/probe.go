// Package rabbitmq holds the RabbitMQ adapters. For now it only has the
// readiness probe used by the api's /readyz; the publisher, consumer and
// topology arrive with asynchronous processing (PLAN.md Phase 2.3).
package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// defaultProbeTimeout bounds a probe whose context has no deadline.
const defaultProbeTimeout = 5 * time.Second

// Probe checks that the broker accepts connections: it dials, opens a
// channel and closes both. It keeps no connection between calls.
type Probe struct {
	url string
}

// NewProbe returns a Probe for the AMQP URL (amqp:// or amqps://). It fails
// when the URL cannot be parsed; it does not contact the broker.
func NewProbe(url string) (*Probe, error) {
	if _, err := amqp.ParseURI(url); err != nil {
		return nil, fmt.Errorf("rabbitmq: invalid URL: %w", err)
	}
	return &Probe{url: url}, nil
}

// Ping dials the broker and opens a channel, within ctx's deadline (or 5 s
// when it has none).
func (p *Probe) Ping(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultProbeTimeout)
	}
	conn, err := amqp.DialConfig(p.url, amqp.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
		Dial: func(network, addr string) (net.Conn, error) {
			var d net.Dialer
			dctx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			c, err := d.DialContext(dctx, network, addr)
			if err != nil {
				return nil, err
			}
			// Bounds the AMQP handshake; cleared once it completes.
			if err := c.SetDeadline(deadline); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		},
		Properties: amqp.Table{"connection_name": "readiness-probe"},
	})
	if err != nil {
		return fmt.Errorf("rabbitmq: dial: %w", err)
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
