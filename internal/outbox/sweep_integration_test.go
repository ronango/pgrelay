//go:build integration

package outbox_test

import (
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/outbox"
	"github.com/ronango/pgrelay/internal/testdb"
)

func TestReclaimOrphans_ExpiredLease(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    2,
		LeasedUntil: time.Now().Add(-5 * time.Second),
	})

	n, err := outbox.ReclaimOrphans(t.Context(), pool)
	if err != nil {
		t.Fatalf("ReclaimOrphans: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d, want 1", n)
	}

	r := getRow(t, pool, id)
	if r.Status != "pending" || r.LeasedUntil != nil {
		t.Errorf("post-reclaim row = %+v, want pending + NULL lease", r)
	}
	if r.LastError != "lease expired" {
		t.Errorf("LastError = %q, want %q", r.LastError, "lease expired")
	}
	if r.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 (not re-incremented)", r.Attempts)
	}
}

func TestReclaimOrphans_NullLease(t *testing.T) {
	// Pathological case: in_flight row with no lease. Claim always
	// sets one, so this shouldn't happen — sweeper recovers anyway.
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{Status: "in_flight", Attempts: 1})

	n, err := outbox.ReclaimOrphans(t.Context(), pool)
	if err != nil {
		t.Fatalf("ReclaimOrphans: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d, want 1", n)
	}
	if r := getRow(t, pool, id); r.Status != "pending" {
		t.Errorf("status = %q, want pending", r.Status)
	}
}

func TestReclaimOrphans_LiveLeaseLeftAlone(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    1,
		LeasedUntil: time.Now().Add(time.Minute),
	})

	n, err := outbox.ReclaimOrphans(t.Context(), pool)
	if err != nil {
		t.Fatalf("ReclaimOrphans: %v", err)
	}
	if n != 0 {
		t.Errorf("reclaimed = %d, want 0", n)
	}
	if r := getRow(t, pool, id); r.Status != "in_flight" {
		t.Errorf("status = %q, want in_flight", r.Status)
	}
}

func TestReclaimOrphans_IgnoresOtherStatuses(t *testing.T) {
	pool := testdb.New(t)
	insertRow(t, pool, insertOpts{Status: "pending"})
	insertRow(t, pool, insertOpts{Status: "sent"})
	insertRow(t, pool, insertOpts{Status: "dead"})

	n, err := outbox.ReclaimOrphans(t.Context(), pool)
	if err != nil {
		t.Fatalf("ReclaimOrphans: %v", err)
	}
	if n != 0 {
		t.Errorf("reclaimed = %d, want 0 (no in_flight rows)", n)
	}
}
