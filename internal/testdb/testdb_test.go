//go:build integration

package testdb_test

import (
	"slices"
	"testing"

	"github.com/ronango/pgrelay/internal/testdb"
)

// TestNew_smoke boots the harness, verifies migrations applied, and asserts
// the pgrelay_outbox index set matches expectations exactly. Exact-match
// (not set-membership) so accidental duplicate indexes from a future
// migration are caught.
func TestNew_smoke(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pgrelay_outbox").Scan(&count); err != nil {
		t.Fatalf("query pgrelay_outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("pgrelay_outbox row count = %d, want 0 (fresh container)", count)
	}

	rows, err := pool.Query(ctx, `
        SELECT indexname FROM pg_indexes
        WHERE tablename = 'pgrelay_outbox'
        ORDER BY indexname
    `)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	want := []string{
		"idx_pgrelay_outbox_aggregate",
		"idx_pgrelay_outbox_in_flight_lease",
		"idx_pgrelay_outbox_pending_due",
		"pgrelay_outbox_pkey",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pgrelay_outbox indexes mismatch:\n  got:  %v\n  want: %v", got, want)
	}
}
