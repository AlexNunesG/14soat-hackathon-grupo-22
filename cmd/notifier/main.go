// Command notifier is the notification service of the video processor
// (RF5): it consumes the video.notify queue (video.failed events) and
// e-mails the owner of each failed video through SMTP, once per event. It
// wires configuration, logging and adapters, and holds no business logic
// (ADR 0002).
//
// It serves GET /healthz and GET /readyz on HEALTH_ADDR. On SIGINT/SIGTERM
// it stops taking events, lets in-flight e-mails finish for up to
// SHUTDOWN_TIMEOUT and requeues the rest.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/adapters/mailer"
	"video-processor/internal/adapters/postgres"
	"video-processor/internal/adapters/rabbitmq"
	"video-processor/internal/app"
	"video-processor/internal/platform/config"
	"video-processor/internal/platform/logging"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintf(os.Stderr, "notifier: unknown arguments %q (usage: notifier)\n", os.Args[1:])
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notifier:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.NotifierFromEnv()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, "notifier", cfg.LogLevel)
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
	smtp, err := mailer.New(mailer.Config{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		TLS:      cfg.SMTP.TLS,
		Timeout:  cfg.SMTP.Timeout,
	})
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	retries, err := rabbitmq.NewPublisher(cfg.Broker.URL, "notifier-retries-"+host, rabbitmq.ExchangeVideos, log, rabbitmq.VideoNotify)
	if err != nil {
		return err
	}
	defer retries.Close()

	notifier := app.NewNotifier(smtp, postgres.NewNotifications(db),
		app.WithNotifierLogger(log), app.WithAppURL(cfg.AppURL))
	consumer, err := rabbitmq.NewConsumer(rabbitmq.ConsumerConfig{
		URL:             cfg.Broker.URL,
		Name:            "notifier",
		Queue:           rabbitmq.VideoNotify,
		Concurrency:     cfg.Concurrency,
		MaxAttempts:     cfg.MaxAttempts,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, notifier, retries, log)
	if err != nil {
		return err
	}

	// The mail server is not a readiness check: while it is down the
	// events wait in the retry queues, and the notifier itself is fine.
	health := httpapi.NewHealthRouter(log, []httpapi.Check{
		{Name: "database", Pinger: db},
		{Name: "broker", Pinger: broker},
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
			stop() // a notifier without probes would look dead: shut down
		}
		healthErr <- err
	}()

	log.Info("notifier started",
		slog.Int("concurrency", cfg.Concurrency),
		slog.String("smtp", net.JoinHostPort(cfg.SMTP.Host, fmt.Sprint(cfg.SMTP.Port))),
		slog.String("smtp_tls", cfg.SMTP.TLS),
		slog.String("health_addr", ln.Addr().String()))
	consumeErr := consumer.Run(ctx)
	stop() // also stops the health server if the consumer returned first
	if err := <-healthErr; err != nil {
		return err
	}
	if consumeErr != nil {
		return consumeErr
	}
	log.Info("notifier stopped")
	return nil
}
