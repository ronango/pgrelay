// Package testdb provides a testcontainers-backed Postgres harness
// shared across pgrelay's integration tests.
//
// Callers receive a ready-to-use *pgxpool.Pool with all migrations applied;
// the container and pool are torn down via t.Cleanup. Callers do not pass a
// context — t.Context() is used so cancellation follows the test lifecycle.
//
// This package must remain test-only: importing it from production code
// would drag golang-migrate's lib/pq driver into the dispatcher binary.
// The integration smoke test file uses //go:build integration so the
// harness is excluded from normal `go test` runs.
package testdb

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // postgres driver registration
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ronango/pgrelay/migrations"
)

// defaultImage is used when PGRELAY_TEST_PG_IMAGE is unset. CI sets it per
// matrix entry (postgres:14-alpine .. postgres:17-alpine); local runs
// default to the highest supported version.
const defaultImage = "postgres:17-alpine"

// New boots a Postgres container, applies all embedded migrations, and
// returns a connection pool. Container teardown and pool close are
// registered with t.Cleanup.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	image := defaultImage
	if v := os.Getenv("PGRELAY_TEST_PG_IMAGE"); v != "" {
		image = v
	}

	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("pgrelay_test"),
		tcpostgres.WithUsername("pgrelay"),
		tcpostgres.WithPassword("pgrelay"),
		tcpostgres.BasicWaitStrategies(),
	)
	// TerminateContainer handles a non-nil container even when err != nil
	// (testcontainers-go may return both on a half-started container).
	t.Cleanup(func() {
		if terr := testcontainers.TerminateContainer(container); terr != nil {
			t.Logf("terminate postgres container: %v", terr)
		}
	})
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	if err = applyMigrations(t, dsn); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func applyMigrations(t testing.TB, dsn string) error {
	t.Helper()

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil || dbErr != nil {
			t.Logf("close migrate: source=%v db=%v", srcErr, dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
