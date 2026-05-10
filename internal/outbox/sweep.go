package outbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReclaimOrphans returns to 'pending' any in_flight row whose lease has
// expired. Safety net for dispatchers that crashed or hung mid-dispatch.
//
// attempts is intentionally not re-incremented (Claim already counted
// the dispatch); next_attempt_at is left alone because it was set by
// the prior MarkRetry (or insert) and is already in the past from
// pending's perspective. last_error is overwritten with a sentinel so
// operators can distinguish lease expiry from sink failures.
//
// `leased_until IS NULL` is included to catch the pathological case
// where an in_flight row somehow lost its lease — Claim always sets
// one, so this shouldn't happen, but a stuck-forever row is the worst
// failure mode and worth one extra OR.
//
// Returns the number of rows reclaimed.
func ReclaimOrphans(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE pgrelay_outbox
		SET status = 'pending',
		    leased_until = NULL,
		    last_error = 'lease expired'
		WHERE status = 'in_flight'
		  AND (leased_until IS NULL OR leased_until < now())
	`)
	if err != nil {
		return 0, fmt.Errorf("reclaim orphans: %w", err)
	}
	return tag.RowsAffected(), nil
}
