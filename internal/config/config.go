// Package config provides a simple environment-variable loader shared by
// all services in the monorepo. It intentionally avoids a config library —
// each service calls Load() and reads the fields it needs.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds environment-derived configuration. Defaults match the local
// dev values used in infra/docker-compose.yml.
type Config struct {
	// Port is the HTTP port the service listens on.
	Port string

	// DatabaseURL is the full Postgres DSN. If DATABASE_URL is not set
	// explicitly, it is composed from the discrete POSTGRES_* vars below.
	DatabaseURL string

	PostgresUser     string
	PostgresPassword string
	PostgresDB       string
	PostgresHost     string
	PostgresPort     string

	// JWTSecret is the HS256 shared secret used to sign and verify tokens.
	// auth-service issues tokens with it; other services (e.g.
	// video-service, in a later delivery) verify tokens with the same
	// secret/value, since they share the same trust boundary.
	JWTSecret string

	// JWTTTLHours is how long an issued token stays valid.
	JWTTTLHours int
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Load reads configuration from environment variables, applying sane
// defaults for local development that match infra/docker-compose.yml's
// service env vars (POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_DB default to
// "fiapx").
func Load() Config {
	cfg := Config{
		Port:             getEnv("PORT", "8081"),
		PostgresUser:     getEnv("POSTGRES_USER", "fiapx"),
		PostgresPassword: getEnv("POSTGRES_PASSWORD", "fiapx"),
		PostgresDB:       getEnv("POSTGRES_DB", "fiapx"),
		PostgresHost:     getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:     getEnv("POSTGRES_PORT", "5432"),
		JWTSecret:        getEnv("JWT_SECRET", "dev-secret-change-me-in-production"),
		JWTTTLHours:      getEnvInt("JWT_TTL_HOURS", 24),
	}

	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cfg.DatabaseURL = dbURL
	} else {
		cfg.DatabaseURL = fmt.Sprintf(
			"postgres://%s:%s@%s:%s/%s?sslmode=disable",
			cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresHost, cfg.PostgresPort, cfg.PostgresDB,
		)
	}

	return cfg
}
