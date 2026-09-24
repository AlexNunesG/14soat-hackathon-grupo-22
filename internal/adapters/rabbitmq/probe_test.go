package rabbitmq

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestNewProbeRejectsInvalidURL(t *testing.T) {
	for _, u := range []string{"", "http://localhost:5672", "amqp://host:notaport"} {
		if _, err := NewProbe(u); err == nil {
			t.Errorf("NewProbe(%q): want error", u)
		}
	}
	if _, err := NewProbe("amqp://guest:guest@localhost:5672/"); err != nil {
		t.Errorf("valid URL: %v", err)
	}
}

func TestPingUnreachable(t *testing.T) {
	// A listener that is closed right away gives a port nobody listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	p, err := NewProbe("amqp://guest:guest@" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err == nil {
		t.Fatal("want error")
	}
}

func TestPingSilentServerTimesOut(t *testing.T) {
	// A server that accepts but never speaks AMQP: the handshake must be
	// cut by the context deadline.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	p, err := NewProbe("amqp://guest:guest@" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Ping(ctx); err == nil {
		t.Fatal("want error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Ping took %s, want it bounded by the context", elapsed)
	}
}

func TestPingCanceledContext(t *testing.T) {
	p, err := NewProbe("amqp://guest:guest@localhost:5672/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Ping(ctx); err == nil {
		t.Fatal("want error")
	}
}

// TestPingRealBroker runs when RABBITMQ_TEST_URL points to a broker, e.g.
// amqp://guest:guest@localhost:5672/ with the compose stack up.
func TestPingRealBroker(t *testing.T) {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set")
	}
	p, err := NewProbe(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
