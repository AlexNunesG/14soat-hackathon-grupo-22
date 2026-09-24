// Command worker is the processing service of the video processor: it
// consumes the video.process queue, extracts each video's frames with
// ffmpeg, zips them and records the outcome (RF1) with its video.failed or
// video.processed event, which an outbox relay publishes (RF5, ADR 0004).
// It wires configuration, logging and adapters, and holds no business
// logic (ADR 0002).
//
// It serves GET /healthz and GET /readyz on HEALTH_ADDR. On SIGINT/SIGTERM
// it stops taking jobs, lets in-flight ones finish for up to
// SHUTDOWN_TIMEOUT and requeues the rest, then publishes the events left
// in the outbox.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"video-processor/internal/adapters/ffmpeg"
	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/adapters/postgres"
	"video-processor/internal/adapters/rabbitmq"
	rediscache "video-processor/internal/adapters/redis"
	"video-processor/internal/adapters/storage"
	"video-processor/internal/adapters/zip"
	"video-processor/internal/app"
	"video-processor/internal/platform/config"
	"video-processor/internal/platform/logging"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintf(os.Stderr, "worker: unknown arguments %q (usage: worker)\n", os.Args[1:])
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.WorkerFromEnv()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, "worker", cfg.LogLevel)
	slog.SetDefault(log)

	// SIGINT/SIGTERM cancel ctx, which starts the graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.NewPool(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := storage.New(storage.Config{
		Endpoint:  cfg.Storage.Endpoint,
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return err
	}
	broker, err := rabbitmq.NewProbe(cfg.Broker.URL)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	retries, err := rabbitmq.NewPublisher(cfg.Broker.URL, "worker-retries-"+host, rabbitmq.ExchangeVideos, log, rabbitmq.VideoProcess)
	if err != nil {
		return err
	}
	defer retries.Close()

	// Final status changes queue their events in the outbox (ADR 0004); the
	// relay publishes them, woken up by every event. It may also publish
	// rows queued by the api, and vice versa: every relay publishes the
	// whole topology.
	events, err := rabbitmq.NewOutboxPublisher(cfg.Broker.URL, "worker-outbox-relay-"+host, log)
	if err != nil {
		return err
	}
	defer events.Close()
	relay := app.NewOutboxRelay(postgres.NewOutbox(db), events, log, cfg.Outbox.Interval, cfg.Outbox.BatchSize)

	opts := []app.ProcessorOption{
		app.WithProcessorTempDir(cfg.TempDir),
		app.WithProcessorLogger(log),
		app.OnEvent(relay.Notify),
	}
	// Every status change invalidates the owner's cached video list
	// (docs/cache.md), best effort: Redis errors never fail a job.
	if cfg.Cache.Enabled() {
		cache, err := rediscache.New(cfg.Cache.URL, cfg.Cache.TTL)
		if err != nil {
			return err
		}
		defer cache.Close()
		opts = append(opts, app.WithProcessorListInvalidator(cache))
	}
	processor := app.NewProcessor(postgres.NewVideos(db), postgres.NewUsers(db), store,
		ffmpeg.New(ffmpeg.WithTimeout(cfg.FFmpegTimeout)), zip.New(), opts...)
	consumer, err := rabbitmq.NewConsumer(rabbitmq.ConsumerConfig{
		URL:             cfg.Broker.URL,
		Name:            "worker",
		Queue:           rabbitmq.VideoProcess,
		Concurrency:     cfg.Concurrency,
		MaxAttempts:     cfg.MaxAttempts,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, processor, retries, log)
	if err != nil {
		return err
	}

	health := httpapi.NewHealthRouter(log, []httpapi.Check{
		{Name: "database", Pinger: db},
		{Name: "broker", Pinger: broker},
		{Name: "storage", Pinger: store},
	}, cfg.ReadinessTimeout)
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HealthAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HealthAddr, err)
	}
	healthErr := make(chan error, 1)
	go func() {
		err := httpapi.Serve(ctx, httpapi.NewServer(cfg.HealthAddr, health), ln, cfg.ShutdownTimeout)
		if err != nil {
			stop() // a worker without probes would look dead: shut down
		}
		healthErr <- err
	}()

	// The relay outlives the consumer, so the events of the last jobs are
	// published before the process exits.
	relayCtx, stopRelay := context.WithCancel(context.WithoutCancel(ctx))
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.Run(relayCtx)
	}()
	defer func() {
		stopRelay()
		<-relayDone
	}()

	log.Info("worker started", slog.Int("concurrency", cfg.Concurrency), slog.String("health_addr", ln.Addr().String()))
	consumeErr := consumer.Run(ctx)
	stop() // also stops the health server if the consumer returned first
	if err := <-healthErr; err != nil {
		return err
	}
	if consumeErr != nil {
		return consumeErr
	}
	log.Info("worker stopped")
	return nil
}
