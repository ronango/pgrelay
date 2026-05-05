package pgconn

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// validBaseline is a Config with all fields set to legal values; tests
// mutate one field at a time to isolate which validation rule trips.
func validBaseline() Config {
	return Config{
		DSN:               "postgres://user:pass@localhost/db",
		MinConns:          1,
		MaxConns:          10,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
	}
}

func TestConfig_ValidateAccepts(t *testing.T) {
	if err := validBaseline().Validate(); err != nil {
		t.Errorf("baseline valid config: %v", err)
	}
}

func TestConfig_ValidateAcceptsMinEqualsMax(t *testing.T) {
	cfg := validBaseline()
	cfg.MinConns = 5
	cfg.MaxConns = 5
	if err := cfg.Validate(); err != nil {
		t.Errorf("MinConns == MaxConns should be accepted, got: %v", err)
	}
}

// TestNew_TracerRollbackOnFailure exercises the deferred unregister path:
// a failed New() must not leave tracer metrics on the registry, otherwise
// a retry on the same registerer panics from MustRegister duplicate-name.
func TestNew_TracerRollbackOnFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	cfg := validBaseline() // DSN points at localhost; no PG running here.

	// Pre-canceled ctx forces Ping to fail immediately for both calls.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := New(ctx, cfg, reg); err == nil {
		t.Fatal("first New: expected error from canceled ctx, got nil")
	}

	// If rollback worked, the second call's newTracer registers cleanly.
	// If it didn't, MustRegister panics from a duplicate metric name and
	// the test fails. We don't care that the second call also errors at
	// Ping — only that it gets past tracer registration.
	if _, err := New(ctx, cfg, reg); err == nil {
		t.Fatal("second New: expected error from canceled ctx, got nil")
	}
}

func TestConfig_ValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Config)
		wantSub string
	}{
		{"empty_dsn", func(c *Config) { c.DSN = "" }, "DSN"},
		{"negative_min", func(c *Config) { c.MinConns = -1 }, "MinConns"},
		{"zero_max", func(c *Config) { c.MaxConns = 0 }, "MaxConns"},
		{"min_exceeds_max", func(c *Config) { c.MinConns = 20; c.MaxConns = 10 }, "MinConns"},
		{"negative_lifetime", func(c *Config) { c.MaxConnLifetime = -time.Hour }, "MaxConnLifetime"},
		{"negative_idle", func(c *Config) { c.MaxConnIdleTime = -time.Minute }, "MaxConnIdleTime"},
		{"negative_health", func(c *Config) { c.HealthCheckPeriod = -time.Second }, "HealthCheckPeriod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseline()
			tc.mut(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}
