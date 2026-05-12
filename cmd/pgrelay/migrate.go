package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // pgx-compatible db driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/urfave/cli/v3"

	"github.com/ronango/pgrelay/migrations"
)

// migrateCommand groups up/down/status against the embedded migrations.
// DB URL precedence: --database-url flag overrides PGRELAY_DATABASE_URL.
func migrateCommand() *cli.Command {
	return &cli.Command{
		Name:  "migrate",
		Usage: "Apply, roll back, or inspect database migrations",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "database-url",
				Usage:   "Postgres connection string (overrides PGRELAY_DATABASE_URL)",
				Sources: cli.EnvVars("PGRELAY_DATABASE_URL"),
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "up",
				Usage: "Apply pending migrations",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "steps",
						Usage: "Apply only N migrations (0 means all)",
					},
				},
				Action: migrateUpAction,
			},
			{
				Name:  "down",
				Usage: "Roll back migrations (requires --yes)",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "steps",
						Value: 1,
						Usage: "Number of migrations to roll back",
					},
					&cli.BoolFlag{
						Name:  "yes",
						Usage: "Confirm destructive rollback",
					},
				},
				Action: migrateDownAction,
			},
			{
				Name:   "status",
				Usage:  "Print the applied migration version and dirty flag",
				Action: migrateStatusAction,
			},
		},
	}
}

func newMigrator(dsn string) (*migrate.Migrate, error) {
	if dsn == "" {
		return nil, errors.New("PGRELAY_DATABASE_URL or --database-url is required")
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate instance: %w", err)
	}
	return m, nil
}

// closeMigrator surfaces source/db close errors so a failed close on a
// successful command still exits non-zero — leaking a DB connection on
// a one-shot migrate run shouldn't be silent.
func closeMigrator(m *migrate.Migrate) error {
	srcErr, dbErr := m.Close()
	if srcErr == nil && dbErr == nil {
		return nil
	}
	return fmt.Errorf("close migrate: %w", errors.Join(srcErr, dbErr))
}

func migrateUpAction(_ context.Context, cmd *cli.Command) error {
	m, err := newMigrator(cmd.String("database-url"))
	if err != nil {
		return err
	}
	defer func() { _ = closeMigrator(m) }()

	steps := cmd.Int("steps")
	switch {
	case steps > 0:
		err = m.Steps(int(steps))
	default:
		err = m.Up()
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	fmt.Println("migrate up: ok")
	return nil
}

func migrateDownAction(_ context.Context, cmd *cli.Command) error {
	if !cmd.Bool("yes") {
		return errors.New("migrate down requires --yes (destructive)")
	}
	steps := cmd.Int("steps")
	if steps < 1 {
		return fmt.Errorf("--steps must be >= 1, got %d", steps)
	}

	m, err := newMigrator(cmd.String("database-url"))
	if err != nil {
		return err
	}
	defer func() { _ = closeMigrator(m) }()

	if err := m.Steps(-int(steps)); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	fmt.Printf("migrate down: rolled back %d migration(s)\n", steps)
	return nil
}

func migrateStatusAction(_ context.Context, cmd *cli.Command) error {
	m, err := newMigrator(cmd.String("database-url"))
	if err != nil {
		return err
	}
	defer func() { _ = closeMigrator(m) }()

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		fmt.Println("migrate status: no migrations applied")
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate status: %w", err)
	}
	suffix := ""
	if dirty {
		suffix = " (DIRTY — manual repair required)"
	}
	fmt.Printf("migrate status: version=%d%s\n", version, suffix)
	return nil
}
