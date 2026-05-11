package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"
	"golang.org/x/sync/errgroup"

	"github.com/ronango/pgrelay/internal/config"
	"github.com/ronango/pgrelay/internal/health"
	"github.com/ronango/pgrelay/internal/metrics"
	"github.com/ronango/pgrelay/internal/outbox"
	"github.com/ronango/pgrelay/internal/outbox/sinks"
	"github.com/ronango/pgrelay/internal/pgconn"
)

func runCommand() *cli.Command {
	return &cli.Command{
		Name:   "run",
		Usage:  "Start the dispatcher loop (blocks until SIGINT/SIGTERM)",
		Action: runAction,
	}
}

func runAction(ctx context.Context, _ *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	reg := metrics.NewRegistry()

	pool, err := pgconn.New(ctx, pgconn.Config{
		DSN:               cfg.DatabaseURL,
		MinConns:          cfg.DBMinConns,
		MaxConns:          cfg.DBMaxConns,
		MaxConnLifetime:   cfg.DBMaxConnLifetime,
		MaxConnIdleTime:   cfg.DBMaxConnIdleTime,
		HealthCheckPeriod: cfg.DBHealthCheckPeriod,
	}, reg)
	if err != nil {
		return err
	}
	defer pool.Close()

	outbox.RegisterStateCollector(reg, pool, log)
	om := outbox.NewMetrics(reg)

	dispatcher := outbox.New(pool, sinks.NewHTTPSink(), outbox.NewPolicy(cfg), om, cfg, log)
	healthSrv := health.New(cfg.OpsAddr, reg, health.ProbeFunc(pool.Ping))

	log.InfoContext(ctx, "pgrelay starting",
		"version", Version,
		"commit", Commit,
		"ops_addr", cfg.OpsAddr,
		"db_max_conns", cfg.DBMaxConns,
	)

	// errgroup so the first subsystem failure cancels the others.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return healthSrv.Run(gctx) })
	g.Go(func() error { return dispatcher.Run(gctx) })

	// context.Canceled here means SIGINT/SIGTERM — that's the
	// success exit, not an error.
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.InfoContext(ctx, "pgrelay stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	// config.Validate normalizes LogLevel to one of {debug,info,warn,error},
	// so UnmarshalText cannot fail here in practice.
	_ = lvl.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
