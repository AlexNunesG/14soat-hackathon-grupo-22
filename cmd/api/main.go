// Command api is the HTTP API service of the video processor: it wires
// configuration, logging, the dependencies' adapters and the HTTP server.
// It holds no business logic (ADR 0002).
//
// The API is served on HTTP_ADDR; GET /metrics (Prometheus) on the
// internal listener METRICS_ADDR, which is not meant to be published.
//
// Usage:
//
//	api            serve the HTTP API
//	api migrate    apply the pending database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"video-processor/db/migrations"
	"video-processor/internal/adapters/auth"
	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/adapters/postgres"
	"video-processor/internal/adapters/prom"
	"video-processor/internal/adapters/rabbitmq"
	rediscache "video-processor/internal/adapters/redis"
	"video-processor/internal/adapters/storage"
	"video-processor/internal/app"
	"video-processor/internal/platform/config"
	"video-processor/internal/platform/logging"
	"video-processor/internal/platform/metrics"
)

func main() {
	var err error
	switch args := os.Args[1:]; {
	case len(args) == 0:
		err = run()
	case len(args) == 1 && args[0] == "migrate":
		err = migrate()
	default:
		err = fmt.Errorf("unknown arguments %q (usage: api [migrate])", args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// migrate applies the embedded migrations (db/migrations) to DATABASE_URL.
func migrate() error {
	cfg, err := config.MigrateFromEnv()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, "api", cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.NewPool(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	// The database may still be starting (compose starts migrate as soon as
	// postgres reports healthy); wait for it instead of failing the stack.
	if err := postgres.WaitReady(ctx, db, 60*time.Second, time.Second); err != nil {
		return err
	}
	version, err := postgres.Migrate(ctx, db, migrations.FS, log)
	if err != nil {
		return err
	}
	log.Info("database schema up to date", slog.Int64("version", version))
	return nil
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

	// Prometheus metrics (docs/observability.md), served on METRICS_ADDR.
	reg := metrics.NewRegistry("api")

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

	hasher, err := auth.NewBcrypt(auth.DefaultBcryptCost)
	if err != nil {
		return err
	}
	tokens, err := auth.NewJWT(cfg.Auth.JWTSecret, cfg.Auth.TokenTTL)
	if err != nil {
		return err
	}

	// Uploads are recorded with their jobs in the outbox (ADR 0004); the
	// relay publishes them to RabbitMQ, woken up by every upload. The
	// outbox also holds the worker's video events, so the relay publishes
	// the whole topology (rabbitmq.NewOutboxPublisher).
	publisher, err := rabbitmq.NewOutboxPublisher(cfg.Broker.URL, "api-outbox-relay", log)
	if err != nil {
		return err
	}
	defer publisher.Close()
	outbox := postgres.NewOutbox(db)
	outboxMetrics := prom.NewOutbox(reg, outbox.Pending)
	relay := app.NewOutboxRelay(outbox, outboxMetrics.Publisher(publisher), log, cfg.Outbox.Interval, cfg.Outbox.BatchSize)
	videos := postgres.NewVideos(db)

	// The video list cache (docs/cache.md) is optional and best effort:
	// it is not a readiness check, and Redis errors fall back to Postgres.
	var videoOpts []app.VideosOption
	uploadOpts := []app.UploadsOption{app.WithUploadLogger(log), app.OnEnqueued(relay.Notify)}
	if cfg.Cache.Enabled() {
		cache, err := rediscache.New(cfg.Cache.URL, cfg.Cache.TTL)
		if err != nil {
			return err
		}
		defer cache.Close()
		videoOpts = append(videoOpts, app.WithListCache(prom.InstrumentListCache(reg, cache), log))
		uploadOpts = append(uploadOpts, app.WithUploadListInvalidator(cache))
		log.Info("video list cache enabled", slog.Duration("ttl", cfg.Cache.TTL))
	} else {
		log.Info("video list cache disabled (REDIS_URL is empty)")
	}

	handler := httpapi.NewRouter(httpapi.Options{
		Logger: log,
		Checks: []httpapi.Check{
			{Name: "database", Pinger: db},
			{Name: "broker", Pinger: broker},
			{Name: "storage", Pinger: store},
		},
		CheckTimeout:   cfg.ReadinessTimeout,
		Auth:           app.NewAuth(postgres.NewUsers(db), hasher, tokens),
		Tokens:         tokens,
		Videos:         app.NewVideos(videos, store, videoOpts...),
		Uploads:        app.NewUploads(videos, store, uploadOpts...),
		MaxUploadBytes: cfg.Upload.MaxBytes,
		UploadTempDir:  cfg.Upload.TempDir,
		WebUI:          true,
		Metrics:        httpapi.NewMetrics(reg),
	})

	// The relay outlives the HTTP server, so the jobs of the last uploads
	// are published before the process exits.
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

	var lc net.ListenConfig
	metricsLn, err := lc.Listen(ctx, "tcp", cfg.HTTP.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.MetricsAddr, err)
	}
	metricsErr := make(chan error, 1)
	go func() {
		err := httpapi.Serve(ctx, httpapi.NewServer(cfg.HTTP.MetricsAddr, metrics.Mux(reg, nil)), metricsLn, cfg.HTTP.ShutdownTimeout)
		if err != nil {
			stop() // an api without metrics would be blind: shut down
		}
		metricsErr <- err
	}()

	ln, err := lc.Listen(ctx, "tcp", cfg.HTTP.Addr)
	if err != nil {
		stop()
		<-metricsErr
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.Addr, err)
	}
	log.Info("api listening", slog.String("addr", ln.Addr().String()),
		slog.String("metrics_addr", metricsLn.Addr().String()))
	serveErr := httpapi.Serve(ctx, httpapi.NewServer(cfg.HTTP.Addr, handler), ln, cfg.HTTP.ShutdownTimeout)
	stop() // also stops the metrics server if the api server failed
	if err := errors.Join(serveErr, <-metricsErr); err != nil {
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
