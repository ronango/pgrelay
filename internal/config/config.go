// Package config loads pgrelay configuration from environment variables.
package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config holds runtime configuration for pgrelay.
type Config struct {
	DatabaseURL string `env:"PGRELAY_DATABASE_URL,required,notEmpty"`
	LogLevel    string `env:"PGRELAY_LOG_LEVEL" envDefault:"info"`
}

// validLogLevels is the strict subset accepted by every candidate logger
// (slog / zap / zerolog). Widen here if the observability layer picks one
// with more levels (zerolog adds trace/fatal, zap adds dpanic/fatal).
var validLogLevels = []string{"debug", "info", "warn", "error"}

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
	return nil
}
