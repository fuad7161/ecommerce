// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env      string // development | production
	HTTPAddr string
	AppURL   string // public base URL, e.g. https://shop.example.com

	DatabaseURL string
	RedisURL    string

	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	MinIOUseSSL    bool

	// SecretKey signs order confirmation links. Must be long and random in production.
	SecretKey string

	CookieSecure bool
	AutoMigrate  bool
	// TrustProxy uses X-Forwarded-For / X-Real-IP for the client IP (rate limits, audit).
	// Enable only behind a reverse proxy that overwrites these headers.
	TrustProxy bool

	SteadfastAPIKey    string
	SteadfastSecretKey string
	SteadfastBaseURL   string
	// SteadfastAutoSend creates the consignment automatically when an admin confirms an order.
	SteadfastAutoSend     bool
	SteadfastSyncInterval time.Duration

	CacheTTL time.Duration

	SessionLifetime time.Duration
	MaxUploadBytes  int64
}

func Load() (*Config, error) {
	c := &Config{
		Env:            env("APP_ENV", "development"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		AppURL:         strings.TrimRight(env("APP_URL", "http://localhost:8080"), "/"),
		DatabaseURL:    env("DATABASE_URL", ""),
		RedisURL:       env("REDIS_URL", "redis://localhost:6379/0"),
		MinIOEndpoint:  env("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey: env("MINIO_ACCESS_KEY", ""),
		MinIOSecretKey: env("MINIO_SECRET_KEY", ""),
		MinIOBucket:    env("MINIO_BUCKET", "ecommerce"),
		MinIOUseSSL:    envBool("MINIO_USE_SSL", false),
		SecretKey:      env("SECRET_KEY", ""),
		CookieSecure:   envBool("COOKIE_SECURE", false),
		AutoMigrate:    envBool("AUTO_MIGRATE", true),
		TrustProxy:     envBool("TRUST_PROXY", false),

		SteadfastAPIKey:       env("STEADFAST_API_KEY", ""),
		SteadfastSecretKey:    env("STEADFAST_SECRET_KEY", ""),
		SteadfastBaseURL:      env("STEADFAST_BASE_URL", "https://portal.packzy.com/api/v1"),
		SteadfastAutoSend:     envBool("STEADFAST_AUTO_SEND", false),
		SteadfastSyncInterval: envDuration("STEADFAST_SYNC_INTERVAL", 30*time.Minute),

		CacheTTL:        envDuration("CACHE_TTL", 60*time.Second),
		SessionLifetime: envDuration("SESSION_LIFETIME", 12*time.Hour),
		MaxUploadBytes:  int64(envInt("MAX_UPLOAD_MB", 5)) << 20,
	}

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.MinIOAccessKey == "" || c.MinIOSecretKey == "" {
		missing = append(missing, "MINIO_ACCESS_KEY/MINIO_SECRET_KEY")
	}
	if len(c.SecretKey) < 32 {
		missing = append(missing, "SECRET_KEY (min 32 chars)")
	}
	if c.IsProduction() && !strings.HasPrefix(c.AppURL, "https://") {
		missing = append(missing, "APP_URL must be https:// in production")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing or invalid config: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

func (c *Config) SteadfastConfigured() bool {
	return c.SteadfastAPIKey != "" && c.SteadfastSecretKey != ""
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
