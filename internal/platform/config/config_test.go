package config_test

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"video-processor/internal/platform/config"
)

// Test-only values for the variables that have no default.
const (
	testDatabaseURL = "postgres://test@db:5432/test"
	testAMQPURL     = "amqp://mq:5672/"
	testAccessKey   = "test-access"
	testSecretKey   = "test-secret"
	testJWTSecret   = "test-jwt-secret-0123456789abcdefghij"
)

// env returns a getenv with the required variables set, then vars (which
// may override them; "" unsets).
func env(vars map[string]string) func(string) string {
	all := map[string]string{
		"DATABASE_URL":  testDatabaseURL,
		"AMQP_URL":      testAMQPURL,
		"S3_ACCESS_KEY": testAccessKey,
		"S3_SECRET_KEY": testSecretKey,
		"JWT_SECRET":    testJWTSecret,
	}
	for k, v := range vars {
		all[k] = v
	}
	return func(k string) string { return all[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{
		HTTP:     config.HTTP{Addr: ":8080", ShutdownTimeout: 15 * time.Second},
		LogLevel: slog.LevelInfo,
		Database: config.Database{URL: testDatabaseURL},
		Broker:   config.Broker{URL: testAMQPURL},
		Storage: config.Storage{
			Endpoint: "localhost:8333", AccessKey: testAccessKey, SecretKey: testSecretKey,
			Bucket: "videos", Region: "us-east-1",
		},
		Auth:             config.Auth{JWTSecret: []byte(testJWTSecret), TokenTTL: time.Hour},
		ReadinessTimeout: 2 * time.Second,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"HTTP_ADDR":         "0.0.0.0:9090",
		"SHUTDOWN_TIMEOUT":  "30s",
		"LOG_LEVEL":         "debug",
		"DATABASE_URL":      "postgresql://db:5432/x",
		"AMQP_URL":          "amqps://mq:5671/vh",
		"S3_ENDPOINT":       "https://s3.example.com",
		"S3_ACCESS_KEY":     "ak",
		"S3_SECRET_KEY":     "sk",
		"S3_BUCKET":         "my.bucket-1",
		"S3_REGION":         "sa-east-1",
		"S3_USE_SSL":        "true",
		"READINESS_TIMEOUT": "500ms",
		"JWT_SECRET":        " another-secret-of-32-bytes-or-more ",
		"JWT_TTL":           "15m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != "0.0.0.0:9090" || cfg.HTTP.ShutdownTimeout != 30*time.Second ||
		cfg.LogLevel != slog.LevelDebug || cfg.Database.URL != "postgresql://db:5432/x" ||
		cfg.Broker.URL != "amqps://mq:5671/vh" || cfg.Storage.Endpoint != "https://s3.example.com" ||
		cfg.Storage.AccessKey != "ak" || cfg.Storage.SecretKey != "sk" || cfg.Storage.Bucket != "my.bucket-1" ||
		cfg.Storage.Region != "sa-east-1" || !cfg.Storage.UseSSL || cfg.ReadinessTimeout != 500*time.Millisecond ||
		string(cfg.Auth.JWTSecret) != " another-secret-of-32-bytes-or-more " || cfg.Auth.TokenTTL != 15*time.Minute {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadBlankUsesDefault(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"HTTP_ADDR": "   "}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("Addr = %q", cfg.HTTP.Addr)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key, value string
	}{
		{"HTTP_ADDR", "8080"},
		{"HTTP_ADDR", ":http-alt"},
		{"HTTP_ADDR", ":70000"},
		{"SHUTDOWN_TIMEOUT", "15"},
		{"SHUTDOWN_TIMEOUT", "-1s"},
		{"LOG_LEVEL", "loud"},
		{"DATABASE_URL", "mysql://u:p@db/x"},
		{"DATABASE_URL", "not a url"},
		{"AMQP_URL", "http://mq:5672"},
		{"S3_BUCKET", "Videos"},
		{"S3_BUCKET", "ab"},
		{"S3_USE_SSL", "maybe"},
		{"READINESS_TIMEOUT", "0s"},
		{"JWT_SECRET", "too-short"},
		{"JWT_TTL", "1h30"},
		{"JWT_TTL", "500ms"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := config.Load(env(map[string]string{tt.key: tt.value}))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name %s", err, tt.key)
			}
		})
	}
}

func TestLoadRequiresCredentials(t *testing.T) {
	_, err := config.Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{"DATABASE_URL", "AMQP_URL", "S3_ACCESS_KEY", "S3_SECRET_KEY", "JWT_SECRET"} {
		if !strings.Contains(err.Error(), key+": is required") {
			t.Errorf("error does not require %s: %v", key, err)
		}
	}
}

func TestLoadReportsEveryError(t *testing.T) {
	_, err := config.Load(env(map[string]string{"HTTP_ADDR": "x", "LOG_LEVEL": "x", "S3_USE_SSL": "x"}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{"HTTP_ADDR", "LOG_LEVEL", "S3_USE_SSL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestLoadDoesNotLeakURLSecrets(t *testing.T) {
	_, err := config.Load(env(map[string]string{"DATABASE_URL": "mysql://user:" + "hunter2" + "@db/x"}))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadDoesNotLeakJWTSecret(t *testing.T) {
	_, err := config.Load(env(map[string]string{"JWT_SECRET": "hunter2-short"}))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadMigrate(t *testing.T) {
	// Only the database is needed: no broker, storage or JWT settings.
	cfg, err := config.LoadMigrate(func(k string) string {
		return map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": "warn"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != testDatabaseURL || cfg.LogLevel != slog.LevelWarn {
		t.Errorf("got %+v", cfg)
	}
	if _, err := config.LoadMigrate(func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "DATABASE_URL: is required") {
		t.Errorf("err = %v, want DATABASE_URL required", err)
	}
}
