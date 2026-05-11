//go:build integration

package outbox_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testRow narrower than outbox.Row so helpers don't depend on the
// unexported scan plumbing.
type testRow struct {
	ID            int64
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LeasedUntil   *time.Time
	LastError     string
}

// insertOpts: zero values fall back to schema defaults (status='pending',
// attempts=0, next_attempt_at=now(), leased_until=NULL).
type insertOpts struct {
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	Sink          string
	Destination   string

	// Optional overrides — leave zero to accept the schema default.
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LeasedUntil   time.Time
}

// insertRow defaults produce a dispatchable pending row pointed at sink="http".
func insertRow(t testing.TB, pool *pgxpool.Pool, o insertOpts) int64 {
	t.Helper()
	if o.AggregateType == "" {
		o.AggregateType = "order"
	}
	if o.AggregateID == "" {
		o.AggregateID = "agg-1"
	}
	if o.EventType == "" {
		o.EventType = "created"
	}
	if o.Payload == nil {
		o.Payload = []byte(`{"k":"v"}`)
	}
	if o.Sink == "" {
		o.Sink = "http"
	}
	if o.Destination == "" {
		o.Destination = "https://example.test/hook"
	}
	if o.Status == "" {
		o.Status = "pending"
	}

	nextAt := pgtype.Timestamptz{Time: o.NextAttemptAt, Valid: !o.NextAttemptAt.IsZero()}
	leased := pgtype.Timestamptz{Time: o.LeasedUntil, Valid: !o.LeasedUntil.IsZero()}

	const sql = `
		INSERT INTO pgrelay_outbox (
			aggregate_type, aggregate_id, event_type, payload,
			sink, destination, status, attempts,
			next_attempt_at, leased_until
		) VALUES (
			$1, $2, $3, $4::jsonb,
			$5, $6, $7, $8,
			COALESCE($9::timestamptz, now()),
			$10::timestamptz
		)
		RETURNING id
	`
	var id int64
	err := pool.QueryRow(t.Context(), sql,
		o.AggregateType, o.AggregateID, o.EventType, o.Payload,
		o.Sink, o.Destination, o.Status, o.Attempts,
		nextAt, leased,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insertRow: %v", err)
	}
	return id
}

func getRow(t testing.TB, pool *pgxpool.Pool, id int64) testRow {
	t.Helper()
	var r testRow
	var leased pgtype.Timestamptz
	var lastError pgtype.Text
	err := pool.QueryRow(t.Context(), `
		SELECT id, status, attempts, next_attempt_at, leased_until, last_error
		FROM pgrelay_outbox
		WHERE id = $1
	`, id).Scan(&r.ID, &r.Status, &r.Attempts, &r.NextAttemptAt, &leased, &lastError)
	if err != nil {
		t.Fatalf("getRow %d: %v", id, err)
	}
	if leased.Valid {
		r.LeasedUntil = &leased.Time
	}
	r.LastError = lastError.String
	return r
}

// waitForStatus synchronizes on Run's async work; polls every 20ms
// until status==want or timeout fires.
func waitForStatus(t testing.TB, pool *pgxpool.Pool, id int64, want string, timeout time.Duration) testRow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		r := getRow(t, pool, id)
		if r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("row %d: status=%q after %s, want %q", id, r.Status, timeout, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
