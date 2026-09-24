package rabbitmq

import (
	"context"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Connection settings shared by every connection the adapters open.
const (
	// defaultDialTimeout bounds a dial whose context has no deadline.
	defaultDialTimeout = 5 * time.Second
	heartbeat          = 10 * time.Second
)

// validateURL checks an AMQP URL (amqp:// or amqps://) without contacting
// the broker.
func validateURL(url string) error {
	if _, err := amqp.ParseURI(url); err != nil {
		return fmt.Errorf("rabbitmq: invalid URL: %w", err)
	}
	return nil
}

// dial opens a connection named name (shown in the management UI). The TCP
// connect and the AMQP handshake are bounded by ctx's deadline, or by
// defaultDialTimeout when it has none; ctx does not affect the connection
// once it is open.
func dial(ctx context.Context, url, name string) (*amqp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultDialTimeout)
	}
	conn, err := amqp.DialConfig(url, amqp.Config{
		Heartbeat: heartbeat,
		Locale:    "en_US",
		Dial: func(network, addr string) (net.Conn, error) {
			var d net.Dialer
			dctx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			c, err := d.DialContext(dctx, network, addr)
			if err != nil {
				return nil, err
			}
			// Bounds the AMQP handshake; amqp091 clears it once the
			// handshake completes.
			if err := c.SetDeadline(deadline); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		},
		Properties: amqp.Table{"connection_name": name},
	})
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial: %w", err)
	}
	return conn, nil
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
