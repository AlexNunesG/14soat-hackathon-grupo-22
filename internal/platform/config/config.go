// Package config loads service configuration from environment variables,
// with defaults for non-secret settings, and validates it. Every variable is documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"video-processor/internal/platform/logging"
)

// Config is the configuration of the api service. Sections are shared with
// the worker (Worker) and the notifier as they arrive.
type Config struct {
	HTTP     HTTP
	LogLevel slog.Level
	Database Database
	Broker   Broker
	Storage  Storage
	Auth     Auth
	Upload   Upload
	Outbox   Outbox
	Cache    Cache
	// ReadinessTimeout bounds each dependency check of GET /readyz.
	ReadinessTimeout time.Duration
}

// Upload configures POST /api/v1/videos.
type Upload struct {
	// MaxBytes limits the size of an upload request (MAX_UPLOAD_BYTES).
	MaxBytes int64
	// TempDir is where uploads are spooled while received
	// (UPLOAD_TEMP_DIR; "" is the system's temp dir).
	TempDir string
}

// Outbox configures the relay that publishes the outbox (ADR 0004).
type Outbox struct {
	// Interval is the relay's poll interval (OUTBOX_POLL_INTERVAL); uploads
	// also wake it up at once.
	Interval time.Duration
	// BatchSize is the number of messages per relay transaction
	// (OUTBOX_BATCH_SIZE, 1 to 500).
	BatchSize int
}

// Cache configures the Redis cache of the video lists (docs/cache.md).
type Cache struct {
	// URL is the Redis URL (REDIS_URL, redis:// or rediss://); "" disables
	// the cache.
	URL string
	// TTL is how long a cached page lives (CACHE_TTL).
	TTL time.Duration
}

// Enabled reports whether the cache is configured.
func (c Cache) Enabled() bool { return c.URL != "" }

// Worker is the configuration of the worker service.
type Worker struct {
	LogLevel slog.Level
	Database Database
	Broker   Broker
	Storage  Storage
	// Cache is the list cache the worker invalidates on every status
	// change; only URL is used.
	Cache Cache
	// HealthAddr is where GET /healthz and /readyz are served
	// (HEALTH_ADDR, host:port).
	HealthAddr string
	// Concurrency is the number of videos processed at the same time, and
	// the prefetch count (WORKER_CONCURRENCY).
	Concurrency int
	// MaxAttempts is how many times a job is tried before its video ends
	// FAILED (WORKER_MAX_ATTEMPTS; 0 means as many as the retry queues
	// allow).
	MaxAttempts int
	// FFmpegTimeout bounds the frame extraction of one video
	// (FFMPEG_TIMEOUT).
	FFmpegTimeout time.Duration
	// TempDir is where jobs keep their files (WORKER_TEMP_DIR; "" is the
	// system's temp dir).
	TempDir string
	// ShutdownTimeout is how long in-flight jobs may finish after
	// SIGINT/SIGTERM before they are requeued (SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// ReadinessTimeout bounds each dependency check of GET /readyz.
	ReadinessTimeout time.Duration
	// Outbox configures the relay that publishes the video.failed and
	// video.processed events the worker records (ADR 0004).
	Outbox Outbox
}

// Notifier is the configuration of the notifier service (RF5).
type Notifier struct {
	LogLevel slog.Level
	Database Database
	Broker   Broker
	SMTP     SMTP
	// AppURL is the link to the web UI written in the e-mails (APP_URL).
	AppURL string
	// HealthAddr is where GET /healthz and /readyz are served
	// (HEALTH_ADDR, host:port).
	HealthAddr string
	// Concurrency is the number of e-mails sent at the same time, and the
	// prefetch count (NOTIFIER_CONCURRENCY).
	Concurrency int
	// MaxAttempts is how many times an e-mail is tried before its event is
	// dead-lettered (NOTIFIER_MAX_ATTEMPTS; 0 means as many as the retry
	// queues allow).
	MaxAttempts int
	// ShutdownTimeout is how long in-flight e-mails may finish after
	// SIGINT/SIGTERM before they are requeued (SHUTDOWN_TIMEOUT).
	ShutdownTimeout time.Duration
	// ReadinessTimeout bounds each dependency check of GET /readyz.
	ReadinessTimeout time.Duration
}

// SMTP configures the mail server the notifier sends through.
type SMTP struct {
	Host string // SMTP_HOST
	Port int    // SMTP_PORT
	// Username and Password (SMTP_USERNAME, SMTP_PASSWORD) enable PLAIN
	// authentication when Username is set; it needs TLS.
	Username string
	Password string
	// From is the sender address (SMTP_FROM), "Name <addr>" or "addr".
	From string
	// TLS is none, starttls or tls (SMTP_TLS).
	TLS string
	// Timeout bounds the sending of one e-mail (SMTP_TIMEOUT).
	Timeout time.Duration
}

// SMTP TLS modes (SMTP_TLS).
var smtpTLSModes = []string{"none", "starttls", "tls"}

// HTTP configures the API server.
type HTTP struct {
	Addr string // HTTP_ADDR, host:port
	// ShutdownTimeout bounds the graceful shutdown: in-flight requests get
	// this long to finish after SIGINT/SIGTERM.
	ShutdownTimeout time.Duration
}

// Database configures PostgreSQL.
type Database struct {
	URL string // postgres://user:pass@host:port/db?sslmode=...
}

// Broker configures RabbitMQ.
type Broker struct {
	URL string // amqp://user:pass@host:port/vhost
}

// Storage configures the S3-compatible object store.
type Storage struct {
	Endpoint  string // host:port or http(s)://host:port
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// Auth configures the access tokens.
type Auth struct {
	// JWTSecret is the HS256 signing key (JWT_SECRET, at least
	// MinJWTSecretLength bytes). Every api replica must share it.
	JWTSecret []byte
	// TokenTTL is the lifetime of an access token (JWT_TTL).
	TokenTTL time.Duration
}

// MinJWTSecretLength is the minimum length of JWT_SECRET in bytes (the
// HS256 key must be at least as long as the hash output, RFC 7518 §3.2).
const MinJWTSecretLength = 32

// defaults apply to unset variables. Variables that carry credentials
// (DATABASE_URL, AMQP_URL, S3_ACCESS_KEY, S3_SECRET_KEY, JWT_SECRET) have no
// default and
// are required: the compose stack sets them, and .env.example has
// development values for running a service from the host.
var defaults = map[string]string{
	"HTTP_ADDR":         ":8080",
	"SHUTDOWN_TIMEOUT":  "15s",
	"LOG_LEVEL":         "info",
	"S3_ENDPOINT":       "localhost:8333",
	"S3_BUCKET":         "videos",
	"S3_REGION":         "us-east-1",
	"S3_USE_SSL":        "false",
	"READINESS_TIMEOUT": "2s",
	"JWT_TTL":           "1h",
	"CACHE_TTL":         "30s",

	"MAX_UPLOAD_BYTES":     "1073741824", // 1 GiB
	"OUTBOX_POLL_INTERVAL": "1s",
	"OUTBOX_BATCH_SIZE":    "100",

	"HEALTH_ADDR":         ":8081",
	"WORKER_CONCURRENCY":  "2",
	"WORKER_MAX_ATTEMPTS": "0",
	"FFMPEG_TIMEOUT":      "10m",

	"NOTIFIER_CONCURRENCY":  "4",
	"NOTIFIER_MAX_ATTEMPTS": "0",
	"APP_URL":               "http://localhost:8080",
	// Development defaults: MailHog on localhost, no TLS, no auth.
	"SMTP_HOST":    "localhost",
	"SMTP_PORT":    "1025",
	"SMTP_FROM":    "FIAP X Video Processor <no-reply@fiapx.local>",
	"SMTP_TLS":     "none",
	"SMTP_TIMEOUT": "30s",
}

// FromEnv loads the configuration from the process environment.
func FromEnv() (Config, error) { return Load(os.Getenv) }

// Load builds the configuration from getenv, applying defaults to unset or
// empty variables. It reports every invalid variable at once.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}
	cfg := Config{
		HTTP: HTTP{
			Addr:            l.addr("HTTP_ADDR"),
			ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT"),
		},
		LogLevel: l.logLevel(),
		Database: Database{URL: l.url("DATABASE_URL", "postgres", "postgresql")},
		Broker:   Broker{URL: l.url("AMQP_URL", "amqp", "amqps")},
		Storage: Storage{
			Endpoint:  l.str("S3_ENDPOINT"),
			AccessKey: l.required("S3_ACCESS_KEY"),
			SecretKey: l.required("S3_SECRET_KEY"),
			Bucket:    l.bucket("S3_BUCKET"),
			Region:    l.str("S3_REGION"),
			UseSSL:    l.boolean("S3_USE_SSL"),
		},
		Auth: Auth{
			JWTSecret: l.secret("JWT_SECRET", MinJWTSecretLength),
			TokenTTL:  l.durationAtLeast("JWT_TTL", time.Second),
		},
		Upload: Upload{
			MaxBytes: l.int64Between("MAX_UPLOAD_BYTES", 1, 1<<40),
			TempDir:  l.str("UPLOAD_TEMP_DIR"),
		},
		Outbox:           l.outbox(),
		Cache:            l.cache(),
		ReadinessTimeout: l.duration("READINESS_TIMEOUT"),
	}
	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// WorkerFromEnv loads the worker configuration from the process
// environment.
func WorkerFromEnv() (Worker, error) { return LoadWorker(os.Getenv) }

// LoadWorker is Load for the worker service.
func LoadWorker(getenv func(string) string) (Worker, error) {
	l := loader{getenv: getenv}
	cfg := Worker{
		LogLevel: l.logLevel(),
		Database: Database{URL: l.url("DATABASE_URL", "postgres", "postgresql")},
		Broker:   Broker{URL: l.url("AMQP_URL", "amqp", "amqps")},
		Storage: Storage{
			Endpoint:  l.str("S3_ENDPOINT"),
			AccessKey: l.required("S3_ACCESS_KEY"),
			SecretKey: l.required("S3_SECRET_KEY"),
			Bucket:    l.bucket("S3_BUCKET"),
			Region:    l.str("S3_REGION"),
			UseSSL:    l.boolean("S3_USE_SSL"),
		},
		Cache:            l.cache(),
		HealthAddr:       l.addr("HEALTH_ADDR"),
		Concurrency:      int(l.int64Between("WORKER_CONCURRENCY", 1, 256)),
		MaxAttempts:      int(l.int64Between("WORKER_MAX_ATTEMPTS", 0, 100)),
		FFmpegTimeout:    l.duration("FFMPEG_TIMEOUT"),
		TempDir:          l.str("WORKER_TEMP_DIR"),
		ShutdownTimeout:  l.duration("SHUTDOWN_TIMEOUT"),
		ReadinessTimeout: l.duration("READINESS_TIMEOUT"),
		Outbox:           l.outbox(),
	}
	if err := errors.Join(l.errs...); err != nil {
		return Worker{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// NotifierFromEnv loads the notifier configuration from the process
// environment.
func NotifierFromEnv() (Notifier, error) { return LoadNotifier(os.Getenv) }

// LoadNotifier is Load for the notifier service.
func LoadNotifier(getenv func(string) string) (Notifier, error) {
	l := loader{getenv: getenv}
	cfg := Notifier{
		LogLevel:         l.logLevel(),
		Database:         Database{URL: l.url("DATABASE_URL", "postgres", "postgresql")},
		Broker:           Broker{URL: l.url("AMQP_URL", "amqp", "amqps")},
		SMTP:             l.smtp(),
		AppURL:           l.url("APP_URL", "http", "https"),
		HealthAddr:       l.addr("HEALTH_ADDR"),
		Concurrency:      int(l.int64Between("NOTIFIER_CONCURRENCY", 1, 64)),
		MaxAttempts:      int(l.int64Between("NOTIFIER_MAX_ATTEMPTS", 0, 100)),
		ShutdownTimeout:  l.duration("SHUTDOWN_TIMEOUT"),
		ReadinessTimeout: l.duration("READINESS_TIMEOUT"),
	}
	if err := errors.Join(l.errs...); err != nil {
		return Notifier{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// Migrate is the configuration of `api migrate`, which only needs the
// database.
type Migrate struct {
	LogLevel slog.Level
	Database Database
}

// MigrateFromEnv loads the migrate configuration from the process
// environment.
func MigrateFromEnv() (Migrate, error) { return LoadMigrate(os.Getenv) }

// LoadMigrate is Load for `api migrate`: LOG_LEVEL and DATABASE_URL only.
func LoadMigrate(getenv func(string) string) (Migrate, error) {
	l := loader{getenv: getenv}
	cfg := Migrate{
		LogLevel: l.logLevel(),
		Database: Database{URL: l.url("DATABASE_URL", "postgres", "postgresql")},
	}
	if err := errors.Join(l.errs...); err != nil {
		return Migrate{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) str(key string) string {
	if v := strings.TrimSpace(l.getenv(key)); v != "" {
		return v
	}
	return defaults[key]
}

func (l *loader) required(key string) string {
	v := l.str(key)
	if v == "" {
		l.fail(key, "is required")
	}
	return v
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (l *loader) addr(key string) string {
	v := l.str(key)
	if _, port, err := net.SplitHostPort(v); err != nil {
		l.fail(key, "%q is not host:port", v)
	} else if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		l.fail(key, "%q has an invalid port", v)
	}
	return v
}

func (l *loader) duration(key string) time.Duration {
	v := l.str(key)
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail(key, "%q is not a positive duration (e.g. 15s)", v)
	}
	return d
}

func (l *loader) durationAtLeast(key string, minimum time.Duration) time.Duration {
	v := l.str(key)
	d, err := time.ParseDuration(v)
	if err != nil || d < minimum {
		l.fail(key, "%q is not a duration of at least %s (e.g. 1h)", v, minimum)
	}
	return d
}

// secret reads a required secret of at least minLen bytes. Its value is
// never echoed in errors. Surrounding whitespace is not trimmed: it is part
// of the key.
func (l *loader) secret(key string, minLen int) []byte {
	v := l.getenv(key)
	switch {
	case v == "":
		l.fail(key, "is required")
	case len(v) < minLen:
		l.fail(key, "must have at least %d bytes, got %d", minLen, len(v))
	}
	return []byte(v)
}

func (l *loader) int64Between(key string, lo, hi int64) int64 {
	v := l.str(key)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo || n > hi {
		l.fail(key, "%q is not an integer between %d and %d", v, lo, hi)
	}
	return n
}

func (l *loader) boolean(key string) bool {
	v := l.str(key)
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "%q is not a boolean", v)
	}
	return b
}

// logLevel reads LOG_LEVEL, shared by every service.
func (l *loader) logLevel() slog.Level {
	const key = "LOG_LEVEL"
	lvl, err := logging.ParseLevel(l.str(key))
	if err != nil {
		l.fail(key, "%v", err)
	}
	return lvl
}

func (l *loader) url(key string, schemes ...string) string {
	v := l.required(key)
	if v == "" {
		return v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		// Don't echo the value: it may hold a password.
		l.fail(key, "not a valid URL")
		return v
	}
	for _, s := range schemes {
		if u.Scheme == s {
			return v
		}
	}
	l.fail(key, "scheme %q, want one of %s", u.Scheme, strings.Join(schemes, ", "))
	return v
}

// outbox reads OUTBOX_POLL_INTERVAL and OUTBOX_BATCH_SIZE.
func (l *loader) outbox() Outbox {
	return Outbox{
		Interval:  l.duration("OUTBOX_POLL_INTERVAL"),
		BatchSize: int(l.int64Between("OUTBOX_BATCH_SIZE", 1, 500)),
	}
}

// smtp reads the SMTP_* variables. SMTP_PASSWORD is never echoed.
func (l *loader) smtp() SMTP {
	c := SMTP{
		Host:     l.required("SMTP_HOST"),
		Port:     int(l.int64Between("SMTP_PORT", 1, 65535)),
		Username: l.str("SMTP_USERNAME"),
		Password: l.getenv("SMTP_PASSWORD"),
		From:     l.str("SMTP_FROM"),
		TLS:      strings.ToLower(l.str("SMTP_TLS")),
		Timeout:  l.duration("SMTP_TIMEOUT"),
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		l.fail("SMTP_FROM", "%q is not an e-mail address (e.g. Name <no-reply@example.com>)", c.From)
	}
	if !slices.Contains(smtpTLSModes, c.TLS) {
		l.fail("SMTP_TLS", "%q, want one of %s", c.TLS, strings.Join(smtpTLSModes, ", "))
	}
	switch {
	case c.Username != "" && c.TLS == "none":
		l.fail("SMTP_TLS", "must be starttls or tls when SMTP_USERNAME is set (credentials are never sent in clear text)")
	case c.Username == "" && c.Password != "":
		l.fail("SMTP_USERNAME", "is required when SMTP_PASSWORD is set")
	}
	return c
}

// cache reads REDIS_URL (optional) and CACHE_TTL.
func (l *loader) cache() Cache {
	c := Cache{TTL: l.duration("CACHE_TTL")}
	if strings.TrimSpace(l.getenv("REDIS_URL")) != "" {
		c.URL = l.url("REDIS_URL", "redis", "rediss")
	}
	return c
}

// bucketName follows the S3 bucket naming rules (simplified).
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (l *loader) bucket(key string) string {
	v := l.str(key)
	if !bucketName.MatchString(v) {
		l.fail(key, "%q is not a valid bucket name (3-63 lowercase letters, digits, '.', '-')", v)
	}
	return v
}
