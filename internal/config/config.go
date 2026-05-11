// Package config loads pgrelay configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds runtime configuration for pgrelay.
type Config struct {
	DatabaseURL string `env:"PGRELAY_DATABASE_URL,required,notEmpty"`
	LogLevel    string `env:"PGRELAY_LOG_LEVEL" envDefault:"info"`

	// Database pool sizing — consumed by internal/pgconn.
	DBMinConns          int32         `env:"PGRELAY_DB_MIN_CONNS"           envDefault:"1"`
	DBMaxConns          int32         `env:"PGRELAY_DB_MAX_CONNS"           envDefault:"10"`
	DBMaxConnLifetime   time.Duration `env:"PGRELAY_DB_MAX_CONN_LIFETIME"   envDefault:"1h"`
	DBMaxConnIdleTime   time.Duration `env:"PGRELAY_DB_MAX_CONN_IDLE_TIME"  envDefault:"30m"`
	DBHealthCheckPeriod time.Duration `env:"PGRELAY_DB_HEALTH_CHECK_PERIOD" envDefault:"1m"`

	// Observability — consumed by internal/otel and internal/health.
	// OTLPEndpoint empty disables OTel tracing entirely (opt-in for local dev).
	// OTLPHeaders parsed as comma-separated key=value pairs (e.g. for auth tokens).
	// TracesSampler accepts the OTel SDK names (always_on, traceidratio, etc.)
	// with parentbased_* variants. TracesSamplerArg is the ratio when sampler is *traceidratio*.
	OpsAddr          string            `env:"PGRELAY_OPS_ADDR"                 envDefault:":9090"`
	OTLPEndpoint     string            `env:"PGRELAY_OTEL_OTLP_ENDPOINT"`
	OTLPHeaders      map[string]string `env:"PGRELAY_OTEL_OTLP_HEADERS"        envSeparator:"," envKeyValSeparator:"="`
	TracesSampler    string            `env:"PGRELAY_OTEL_TRACES_SAMPLER"      envDefault:"parentbased_always_on"`
	TracesSamplerArg string            `env:"PGRELAY_OTEL_TRACES_SAMPLER_ARG"`

	// Outbox dispatcher — consumed by internal/outbox.
	// Backoff: delay = min(RetryMax, RetryBase * 2^(attempt-1)) ± RetryJitter.
	PollInterval  time.Duration `env:"PGRELAY_POLL_INTERVAL"  envDefault:"100ms"`
	BatchSize     int32         `env:"PGRELAY_BATCH_SIZE"     envDefault:"100"`
	LeaseDuration time.Duration `env:"PGRELAY_LEASE_DURATION" envDefault:"30s"`
	MaxAttempts   int           `env:"PGRELAY_MAX_ATTEMPTS"   envDefault:"10"`
	RetryBase     time.Duration `env:"PGRELAY_RETRY_BASE"     envDefault:"1s"`
	RetryMax      time.Duration `env:"PGRELAY_RETRY_MAX"      envDefault:"5m"`
	RetryJitter   float64       `env:"PGRELAY_RETRY_JITTER"   envDefault:"0.2"`
}

// maxBatchSize caps PGRELAY_BATCH_SIZE so a misconfigured env var can't
// pull millions of rows into memory in one claim.
const maxBatchSize = 10_000

// validLogLevels is the strict subset accepted by every candidate logger
// (slog / zap / zerolog). Widen here if the observability layer picks one
// with more levels (zerolog adds trace/fatal, zap adds dpanic/fatal).
var validLogLevels = []string{"debug", "info", "warn", "error"}

// validTracesSamplers mirrors the OTEL_TRACES_SAMPLER spec values that the
// SDK auto-config understands. internal/otel decodes the chosen value into
// an sdktrace.Sampler.
var validTracesSamplers = []string{
	"always_on",
	"always_off",
	"traceidratio",
	"parentbased_always_on",
	"parentbased_always_off",
	"parentbased_traceidratio",
}

func samplerNeedsRatio(name string) bool {
	return name == "traceidratio" || name == "parentbased_traceidratio"
}

// Load reads configuration from environment variables and validates it.
// Returns an error for missing required fields, an unparseable DatabaseURL,
// or an unknown LogLevel.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse env: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

// Validate checks Config invariants and normalizes LogLevel to lowercase.
func (c *Config) Validate() error {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("PGRELAY_DATABASE_URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("PGRELAY_DATABASE_URL: unsupported scheme %q (expected postgres:// or postgresql://)", u.Scheme)
	}
	level := strings.ToLower(c.LogLevel)
	if !slices.Contains(validLogLevels, level) {
		return fmt.Errorf("PGRELAY_LOG_LEVEL: invalid value %q (expected one of %s)", c.LogLevel, strings.Join(validLogLevels, ", "))
	}
	c.LogLevel = level

	if c.DBMinConns < 0 {
		return fmt.Errorf("PGRELAY_DB_MIN_CONNS: must be >= 0, got %d", c.DBMinConns)
	}
	if c.DBMaxConns < 1 {
		return fmt.Errorf("PGRELAY_DB_MAX_CONNS: must be >= 1, got %d", c.DBMaxConns)
	}
	if c.DBMinConns > c.DBMaxConns {
		return fmt.Errorf("PGRELAY_DB_MIN_CONNS (%d) must be <= PGRELAY_DB_MAX_CONNS (%d)", c.DBMinConns, c.DBMaxConns)
	}
	if c.DBMaxConnLifetime < 0 {
		return fmt.Errorf("PGRELAY_DB_MAX_CONN_LIFETIME: must be >= 0, got %s", c.DBMaxConnLifetime)
	}
	if c.DBMaxConnIdleTime < 0 {
		return fmt.Errorf("PGRELAY_DB_MAX_CONN_IDLE_TIME: must be >= 0, got %s", c.DBMaxConnIdleTime)
	}
	if c.DBHealthCheckPeriod < 0 {
		return fmt.Errorf("PGRELAY_DB_HEALTH_CHECK_PERIOD: must be >= 0, got %s", c.DBHealthCheckPeriod)
	}

	if _, _, err := net.SplitHostPort(c.OpsAddr); err != nil {
		return fmt.Errorf("PGRELAY_OPS_ADDR: invalid host:port %q: %w", c.OpsAddr, err)
	}

	// http/https here implicitly commits OTLP/HTTP transport (port 4318);
	// gRPC (port 4317) would need a separate scheme/exporter wiring.
	if c.OTLPEndpoint != "" {
		u, err := url.Parse(c.OTLPEndpoint)
		if err != nil {
			return fmt.Errorf("PGRELAY_OTEL_OTLP_ENDPOINT: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("PGRELAY_OTEL_OTLP_ENDPOINT: scheme must be http or https, got %q", u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("PGRELAY_OTEL_OTLP_ENDPOINT: missing host in %q", c.OTLPEndpoint)
		}
	}

	if !slices.Contains(validTracesSamplers, c.TracesSampler) {
		return fmt.Errorf("PGRELAY_OTEL_TRACES_SAMPLER: unknown value %q (expected one of %s)",
			c.TracesSampler, strings.Join(validTracesSamplers, ", "))
	}
	if samplerNeedsRatio(c.TracesSampler) {
		if c.TracesSamplerArg == "" {
			return fmt.Errorf("PGRELAY_OTEL_TRACES_SAMPLER_ARG: required when sampler is %q", c.TracesSampler)
		}
		ratio, err := strconv.ParseFloat(c.TracesSamplerArg, 64)
		if err != nil {
			return fmt.Errorf("PGRELAY_OTEL_TRACES_SAMPLER_ARG: %w", err)
		}
		if ratio < 0 || ratio > 1 {
			return fmt.Errorf("PGRELAY_OTEL_TRACES_SAMPLER_ARG: must be in [0, 1], got %v", ratio)
		}
	} else if c.TracesSamplerArg != "" {
		// Reject set-but-ignored arg explicitly: a no-op env var is a
		// debugging trap when an operator sets it expecting an effect.
		return fmt.Errorf("PGRELAY_OTEL_TRACES_SAMPLER_ARG: must be empty when sampler is %q (only used with *traceidratio)", c.TracesSampler)
	}

	if c.PollInterval <= 0 {
		return fmt.Errorf("PGRELAY_POLL_INTERVAL: must be > 0, got %s", c.PollInterval)
	}
	if c.BatchSize < 1 || c.BatchSize > maxBatchSize {
		return fmt.Errorf("PGRELAY_BATCH_SIZE: must be in [1, %d], got %d", maxBatchSize, c.BatchSize)
	}
	if c.LeaseDuration <= 0 {
		return fmt.Errorf("PGRELAY_LEASE_DURATION: must be > 0, got %s", c.LeaseDuration)
	}
	if c.LeaseDuration <= c.PollInterval {
		return fmt.Errorf("PGRELAY_LEASE_DURATION (%s) must be > PGRELAY_POLL_INTERVAL (%s)", c.LeaseDuration, c.PollInterval)
	}
	if c.MaxAttempts < 1 {
		return fmt.Errorf("PGRELAY_MAX_ATTEMPTS: must be >= 1, got %d", c.MaxAttempts)
	}
	if c.RetryBase <= 0 {
		return fmt.Errorf("PGRELAY_RETRY_BASE: must be > 0, got %s", c.RetryBase)
	}
	if c.RetryMax < c.RetryBase {
		return fmt.Errorf("PGRELAY_RETRY_MAX (%s) must be >= PGRELAY_RETRY_BASE (%s)", c.RetryMax, c.RetryBase)
	}
	if c.RetryJitter < 0 || c.RetryJitter > 1 {
		return fmt.Errorf("PGRELAY_RETRY_JITTER: must be in [0, 1], got %v", c.RetryJitter)
	}

	return nil
}
