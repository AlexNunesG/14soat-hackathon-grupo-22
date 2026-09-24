// Package config loads service configuration from environment variables,
// with defaults for non-secret settings, and validates it. Every variable is documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"video-processor/internal/platform/logging"
)

// Config is the configuration of the api service. Sections are shared with
// the worker and notifier as they arrive.
type Config struct {
	HTTP     HTTP
	LogLevel slog.Level
	Database Database
	Broker   Broker
	Storage  Storage
	// ReadinessTimeout bounds each dependency check of GET /readyz.
	ReadinessTimeout time.Duration
}

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

// defaults apply to unset variables. Variables that carry credentials
// (DATABASE_URL, AMQP_URL, S3_ACCESS_KEY, S3_SECRET_KEY) have no default and
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
		LogLevel: l.logLevel("LOG_LEVEL"),
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
		ReadinessTimeout: l.duration("READINESS_TIMEOUT"),
	}
	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
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

func (l *loader) boolean(key string) bool {
	v := l.str(key)
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "%q is not a boolean", v)
	}
	return b
}

func (l *loader) logLevel(key string) slog.Level {
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

// bucketName follows the S3 bucket naming rules (simplified).
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (l *loader) bucket(key string) string {
	v := l.str(key)
	if !bucketName.MatchString(v) {
		l.fail(key, "%q is not a valid bucket name (3-63 lowercase letters, digits, '.', '-')", v)
	}
	return v
}
