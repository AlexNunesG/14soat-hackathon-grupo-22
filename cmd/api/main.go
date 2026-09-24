// Command api is the HTTP API service of the video processor: it wires
// configuration, logging, the dependencies' adapters and the HTTP server.
// It holds no business logic (ADR 0002).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/adapters/postgres"
	"video-processor/internal/adapters/rabbitmq"
	"video-processor/internal/adapters/storage"
	"video-processor/internal/platform/config"
	"video-processor/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, "api", cfg.LogLevel)
	slog.SetDefault(log)

	// SIGINT/SIGTERM cancel ctx, which starts the graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.NewPool(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer db.Close()

	broker, err := rabbitmq.NewProbe(cfg.Broker.URL)
	if err != nil {
		return err
	}

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
	// The bucket is also created by the storage service at startup; this
	// makes the api work against any S3 backend. It runs in the background
	// so the api starts (and /healthz answers) even while storage is down;
	// /readyz reports storage as failing until the bucket exists.
	go ensureBucket(ctx, log, store)

	handler := httpapi.NewRouter(httpapi.Options{
		Logger: log,
		Checks: []httpapi.Check{
			{Name: "database", Pinger: db},
			{Name: "broker", Pinger: broker},
			{Name: "storage", Pinger: store},
		},
		CheckTimeout: cfg.ReadinessTimeout,
	})

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.Addr, err)
	}
	log.Info("api listening", slog.String("addr", ln.Addr().String()))
	if err := httpapi.Serve(ctx, httpapi.NewServer(cfg.HTTP.Addr, handler), ln, cfg.HTTP.ShutdownTimeout); err != nil {
		return err
	}
	log.Info("api stopped")
	return nil
}

// ensureBucket creates the storage bucket, retrying until it succeeds or
// ctx is done.
func ensureBucket(ctx context.Context, log *slog.Logger, store *storage.Client) {
	const retryEvery = 2 * time.Second
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := store.EnsureBucket(attemptCtx)
		cancel()
		if err == nil {
			log.Info("storage bucket ready", slog.String("bucket", store.Bucket()))
			return
		}
		log.Warn("storage bucket not ready, retrying", slog.String("bucket", store.Bucket()), slog.Any("error", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryEvery):
		}
	}
}
