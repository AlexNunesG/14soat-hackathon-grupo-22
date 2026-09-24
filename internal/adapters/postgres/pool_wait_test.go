package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakyPinger struct {
	failures int
	calls    int
}

func (p *flakyPinger) Ping(context.Context) error {
	p.calls++
	if p.calls <= p.failures {
		return errors.New("connection refused")
	}
	return nil
}

func TestWaitReadyRetriesUntilTheDatabaseAnswers(t *testing.T) {
	p := &flakyPinger{failures: 3}
	if err := WaitReady(context.Background(), p, time.Second, time.Millisecond); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if p.calls != 4 {
		t.Errorf("pinged %d times, want 4", p.calls)
	}
}

func TestWaitReadyGivesUpAfterTheTimeout(t *testing.T) {
	p := &flakyPinger{failures: 1 << 30}
	err := WaitReady(context.Background(), p, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady succeeded against a database that never answers")
	}
	if !errors.Is(err, context.DeadlineExceeded) && p.calls < 2 {
		t.Errorf("gave up after %d pings: %v", p.calls, err)
	}
}
