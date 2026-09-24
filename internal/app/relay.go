package app

import (
	"context"
	"log/slog"
	"time"
)

// Defaults of OutboxRelay.
const (
	DefaultRelayInterval  = time.Second
	DefaultRelayBatchSize = 100
	// relayFinalFlush bounds the last relay pass when the relay stops.
	relayFinalFlush = 5 * time.Second
)

// OutboxRelay publishes the messages queued in the outbox (ADR 0004). It
// runs a pass whenever it is notified (after an upload) and at least every
// interval (for messages left by failed publishes, other replicas or a
// previous run). Passes repeat at once while they find full batches.
type OutboxRelay struct {
	store     OutboxStore
	publisher MessagePublisher
	log       *slog.Logger
	interval  time.Duration
	batchSize int
	wake      chan struct{}
}

// NewOutboxRelay returns a relay; non-positive interval and batchSize use
// the defaults.
func NewOutboxRelay(store OutboxStore, publisher MessagePublisher, log *slog.Logger, interval time.Duration, batchSize int) *OutboxRelay {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if interval <= 0 {
		interval = DefaultRelayInterval
	}
	if batchSize <= 0 {
		batchSize = DefaultRelayBatchSize
	}
	return &OutboxRelay{
		store:     store,
		publisher: publisher,
		log:       log,
		interval:  interval,
		batchSize: batchSize,
		wake:      make(chan struct{}, 1),
	}
}

// Notify asks for a pass as soon as possible. It never blocks.
func (r *OutboxRelay) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run relays until ctx is done, then makes a last short pass so messages
// queued by the final requests are not left for the next start.
func (r *OutboxRelay) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relayFinalFlush)
			r.drain(fctx)
			cancel()
			return
		case <-r.wake:
		case <-timer.C:
		}
		r.drain(ctx)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(r.interval)
	}
}

// drain runs passes while they claim full batches.
func (r *OutboxRelay) drain(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := r.store.Relay(ctx, r.batchSize, r.publish)
		if err != nil {
			if ctx.Err() == nil {
				r.log.WarnContext(ctx, "outbox relay pass failed", slog.Any("error", err))
			}
			return
		}
		if n < r.batchSize {
			return
		}
	}
}

// publish publishes a batch and logs the failures; the store keeps the
// failed messages for a later pass.
func (r *OutboxRelay) publish(ctx context.Context, msgs []Message) []error {
	errs := r.publisher.Publish(ctx, msgs)
	failed, first := 0, -1
	for i, err := range errs {
		if err != nil {
			failed++
			if first < 0 {
				first = i
			}
		}
	}
	if failed > 0 {
		attrs := []slog.Attr{slog.Int("failed", failed), slog.Int("batch", len(msgs)), slog.Any("error", errs[first])}
		if first < len(msgs) {
			// The first failed message identifies the batch in the logs.
			attrs = append(attrs, slog.String("message_id", msgs[first].ID),
				slog.String("request_id", msgs[first].CorrelationID))
		}
		r.log.LogAttrs(ctx, slog.LevelWarn, "outbox messages not published; will retry", attrs...)
	} else {
		r.log.DebugContext(ctx, "outbox messages published", slog.Int("count", len(msgs)))
	}
	return errs
}
