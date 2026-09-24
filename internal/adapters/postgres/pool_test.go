package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewPoolRejectsInvalidDSN(t *testing.T) {
	if _, err := NewPool(context.Background(), "postgres://user@host:notaport/db"); err == nil {
		t.Fatal("want error")
	}
}

func TestNewPoolIsLazy(t *testing.T) {
	// Nothing listens on port 1: creating the pool still succeeds, Ping
	// fails.
	pool, err := NewPool(context.Background(), "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err == nil {
		t.Fatal("Ping: want error")
	}
}

// TestPingRealDatabase runs when POSTGRES_TEST_URL points to a database,
// e.g. the compose stack's.
func TestPingRealDatabase(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_URL")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_URL not set")
	}
	pool, err := NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
