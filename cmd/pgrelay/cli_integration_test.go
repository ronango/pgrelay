//go:build integration

package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ronango/pgrelay/internal/testdb"
)

// TestCLI_Version exercises the version subcommand end-to-end so the
// binary's main → cli wiring + ldflags fallback to runtime/debug VCS
// info both prove out. Unit tests cover versionString() directly.
func TestCLI_Version(t *testing.T) {
	res := runArgs(t, nil, "version")
	if res.err != nil {
		t.Fatalf("pgrelay version: %v\nstderr: %s", res.err, res.stderr)
	}
	for _, want := range []string{"pgrelay", runtime.Version()} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout = %q, missing %q", res.stdout, want)
		}
	}
}

func TestCLI_MigrateLifecycle(t *testing.T) {
	// testdb.New applies migrations on construction; we re-use its DSN
	// to exercise the migrate subcommand's status + down paths against
	// a real PG with the schema already in place.
	pool := testdb.New(t)
	dsn := pool.Config().ConnString()
	env := []string{"PGRELAY_DATABASE_URL=" + dsn}

	if res := runArgs(t, env, "migrate", "status"); res.err != nil ||
		!strings.Contains(res.stdout, "version=") {
		t.Fatalf("migrate status: err=%v stdout=%q stderr=%q", res.err, res.stdout, res.stderr)
	}

	res := runArgs(t, env, "migrate", "down")
	if res.err == nil {
		t.Fatalf("migrate down without --yes succeeded; want error\nstdout=%q", res.stdout)
	}
	if !strings.Contains(res.stderr, "--yes") {
		t.Errorf("migrate down stderr = %q, want mention of --yes", res.stderr)
	}

	if res := runArgs(t, env, "migrate", "down", "--yes"); res.err != nil {
		t.Fatalf("migrate down --yes: %v\nstderr: %s", res.err, res.stderr)
	}

	if res := runArgs(t, env, "migrate", "up"); res.err != nil {
		t.Fatalf("migrate up: %v\nstderr: %s", res.err, res.stderr)
	}
	if res := runArgs(t, env, "migrate", "up"); res.err != nil {
		t.Fatalf("migrate up (idempotent): %v\nstderr: %s", res.err, res.stderr)
	}
}
