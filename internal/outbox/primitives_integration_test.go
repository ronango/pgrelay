//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/outbox"
	"github.com/ronango/pgrelay/internal/testdb"
)

func TestClaim_HappyPath(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{})

	const leaseDuration = 30 * time.Second
	beforeClaim := time.Now()
	rows, err := outbox.Claim(t.Context(), pool, 10, leaseDuration)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("Claim returned %v, want [%d]", rows, id)
	}
	if rows[0].Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", rows[0].Attempts)
	}
	// Returned LeasedUntil must reflect the requested window.
	if want := beforeClaim.Add(leaseDuration); rows[0].LeasedUntil.Before(want.Add(-time.Second)) {
		t.Errorf("returned LeasedUntil = %s, want >= %s", rows[0].LeasedUntil, want)
	}

	r := getRow(t, pool, id)
	if r.Status != "in_flight" || r.LeasedUntil == nil {
		t.Fatalf("post-claim row = %+v, want in_flight with non-NULL leased_until", r)
	}
	if want := beforeClaim.Add(leaseDuration); r.LeasedUntil.Before(want.Add(-time.Second)) {
		t.Errorf("on-disk LeasedUntil = %s, want >= %s", *r.LeasedUntil, want)
	}
}

func TestClaim_TieBreaksByID(t *testing.T) {
	// Two rows with the same next_attempt_at must dispatch in id order
	// per Claim's ORDER BY next_attempt_at, id.
	pool := testdb.New(t)
	at := time.Now().Add(-time.Second)
	idLow := insertRow(t, pool, insertOpts{NextAttemptAt: at})
	idHigh := insertRow(t, pool, insertOpts{NextAttemptAt: at})

	rows, err := outbox.Claim(t.Context(), pool, 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != idLow || rows[1].ID != idHigh {
		t.Errorf("Claim order = %v, want [%d, %d]", rows, idLow, idHigh)
	}
}

func TestClaim_RespectsNextAttemptAt(t *testing.T) {
	pool := testdb.New(t)
	insertRow(t, pool, insertOpts{NextAttemptAt: time.Now().Add(time.Hour)})

	rows, err := outbox.Claim(t.Context(), pool, 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Claim returned %d rows, want 0 (future next_attempt_at)", len(rows))
	}
}

func TestClaim_LimitsBatchSize(t *testing.T) {
	pool := testdb.New(t)
	for range 5 {
		insertRow(t, pool, insertOpts{})
	}

	rows, err := outbox.Claim(t.Context(), pool, 3, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("Claim returned %d rows, want 3 (batch cap)", len(rows))
	}
}

func TestClaim_OrdersByNextAttemptThenID(t *testing.T) {
	pool := testdb.New(t)

	now := time.Now()
	idLater := insertRow(t, pool, insertOpts{NextAttemptAt: now.Add(-100 * time.Millisecond)})
	idEarlier := insertRow(t, pool, insertOpts{NextAttemptAt: now.Add(-time.Second)})

	rows, err := outbox.Claim(t.Context(), pool, 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Claim returned %d rows, want 2", len(rows))
	}
	if rows[0].ID != idEarlier || rows[1].ID != idLater {
		t.Errorf("Claim order = [%d, %d], want [%d, %d] (older next_attempt_at first)",
			rows[0].ID, rows[1].ID, idEarlier, idLater)
	}
}

func TestClaim_SkipsLockedRows(t *testing.T) {
	pool := testdb.New(t)
	id1 := insertRow(t, pool, insertOpts{})
	id2 := insertRow(t, pool, insertOpts{})

	// Hold a row lock from a separate connection. The Claim's
	// FOR UPDATE SKIP LOCKED must pass this row over to id2.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, lockErr := tx.Exec(t.Context(),
		`SELECT id FROM pgrelay_outbox WHERE id = $1 FOR UPDATE`, id1); lockErr != nil {
		t.Fatalf("lock id1: %v", lockErr)
	}

	rows, err := outbox.Claim(t.Context(), pool, 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id2 {
		t.Fatalf("Claim returned %v, want [%d] (id1 locked)", rows, id2)
	}
}

func TestMarkSent_TransitionsAndClearsLease(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    1,
		LeasedUntil: time.Now().Add(30 * time.Second),
	})

	if err := outbox.MarkSent(t.Context(), pool, id); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	r := getRow(t, pool, id)
	if r.Status != "sent" || r.LeasedUntil != nil || r.LastError != "" {
		t.Errorf("post-MarkSent = %+v, want status=sent leased_until=NULL last_error=\"\"", r)
	}
}

func TestMarkRetry_TransitionsAndRecordsError(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    1,
		LeasedUntil: time.Now().Add(30 * time.Second),
	})

	nextAt := time.Now().Add(5 * time.Minute).UTC()
	if err := outbox.MarkRetry(t.Context(), pool, id, nextAt, "503 upstream"); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}

	r := getRow(t, pool, id)
	if r.Status != "pending" || r.LeasedUntil != nil {
		t.Errorf("post-MarkRetry status = %q, leased_until = %v, want pending + NULL", r.Status, r.LeasedUntil)
	}
	if r.LastError != "503 upstream" {
		t.Errorf("LastError = %q, want %q", r.LastError, "503 upstream")
	}
	// Truncate to seconds — TIMESTAMPTZ round-trip may shave sub-µs precision.
	if !r.NextAttemptAt.Round(time.Second).Equal(nextAt.Round(time.Second)) {
		t.Errorf("NextAttemptAt = %s, want %s", r.NextAttemptAt, nextAt)
	}
}

func TestMarkDead_TransitionsAndRecordsError(t *testing.T) {
	pool := testdb.New(t)
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    3,
		LeasedUntil: time.Now().Add(30 * time.Second),
	})

	if err := outbox.MarkDead(t.Context(), pool, id, "400 Bad Request"); err != nil {
		t.Fatalf("MarkDead: %v", err)
	}
	r := getRow(t, pool, id)
	if r.Status != "dead" || r.LeasedUntil != nil {
		t.Errorf("post-MarkDead status=%q leased_until=%v, want dead + NULL", r.Status, r.LeasedUntil)
	}
	if r.LastError != "400 Bad Request" {
		t.Errorf("LastError = %q, want %q", r.LastError, "400 Bad Request")
	}
}
