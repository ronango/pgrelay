package pgconn

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Config holds the parameters for building a pgx connection pool.
// Field names mirror pgxpool.Config; values typically flow from
// internal/config.Config.
type Config struct {
	DSN               string
	MinConns          int32
	MaxConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// Validate checks invariants on a manually-constructed Config.
// internal/config performs equivalent checks at env-load time; this is
// belt-and-suspenders for callers building Config in code (e.g. tests).
func (c Config) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("DSN: required")
	}
	if c.MinConns < 0 {
		return fmt.Errorf("MinConns: must be >= 0, got %d", c.MinConns)
	}
	if c.MaxConns < 1 {
		return fmt.Errorf("MaxConns: must be >= 1, got %d", c.MaxConns)
	}
	if c.MinConns > c.MaxConns {
		return fmt.Errorf("MinConns (%d) must be <= MaxConns (%d)", c.MinConns, c.MaxConns)
	}
	if c.MaxConnLifetime < 0 {
		return fmt.Errorf("MaxConnLifetime: must be >= 0, got %s", c.MaxConnLifetime)
	}
	if c.MaxConnIdleTime < 0 {
		return fmt.Errorf("MaxConnIdleTime: must be >= 0, got %s", c.MaxConnIdleTime)
	}
	if c.HealthCheckPeriod < 0 {
		return fmt.Errorf("HealthCheckPeriod: must be >= 0, got %s", c.HealthCheckPeriod)
	}
	return nil
}

// New builds and returns a *pgxpool.Pool wired with the metrics tracer
// and pool-stats collector. Pings the database before returning; on
// failure closes the pool, unregisters any metrics created during this
// call, and returns the error. Respects ctx cancellation through Ping —
// the pool is closed if Ping is interrupted. Fail-fast: no retry.
//
// Call New at most once per registerer: the metric names are fixed, so
// a second call with the same reg fails with AlreadyRegisteredError.
// For multiple pools, wrap reg with prometheus.WrapRegistererWith so
// each pool's metrics carry distinguishing labels.
//
// Future commits will add OTel tracing on the same ConnConfig.Tracer
// hook — composition will require pgx.MultiTracer.
func New(ctx context.Context, cfg Config, reg prometheus.Registerer) (pool *pgxpool.Pool, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate pgconn config: %w", err)
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	pcfg.HealthCheckPeriod = cfg.HealthCheckPeriod

	tr := newTracer(reg)
	pcfg.ConnConfig.Tracer = tr
	// On any failure after this point, roll back the tracer's registry
	// state so the caller can retry (or so a different registerer can
	// take its place) without an AlreadyRegisteredError.
	defer func() {
		if err != nil {
			reg.Unregister(tr.duration)
			reg.Unregister(tr.errors)
		}
	}()

	pool, err = pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("new pool: %w", err)
	}

	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if err = reg.Register(newPoolStatsCollector(pool)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("register pool stats: %w", err)
	}

	return pool, nil
}
