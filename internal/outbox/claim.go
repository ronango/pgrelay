package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Claim atomically transitions up to batchSize pending rows whose
// next_attempt_at has passed to status='in_flight', extends their lease
// by leaseDuration, increments attempts, and returns the locked rows.
//
// FOR UPDATE SKIP LOCKED makes the operation safe under concurrent
// dispatchers — each row is claimed by exactly one caller. ORDER BY
// includes `id` as a tiebreaker so rows inserted in the same transaction
// (sharing a `next_attempt_at` from `now()`) dispatch deterministically.
func Claim(ctx context.Context, pool *pgxpool.Pool, batchSize int32, leaseDuration time.Duration) ([]Row, error) {
	const sql = `
		UPDATE pgrelay_outbox
		SET status = 'in_flight',
		    leased_until = now() + $2::interval,
		    attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM pgrelay_outbox
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		RETURNING ` + rowColumns

	leaseInterval := pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true}

	rows, err := pool.Query(ctx, sql, batchSize, leaseInterval)
	if err != nil {
		return nil, fmt.Errorf("claim query: %w", err)
	}
	return pgx.CollectRows(rows, scanRow)
}
