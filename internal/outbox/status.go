package outbox

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxLastErrorBytes ≈ HTTP error snippet (512) + wrapping headroom.
const maxLastErrorBytes = 4096

// sanitizeLastError strips control bytes (TTY-escape footgun) and
// caps length. Double-truncate because strings.Map decodes invalid
// UTF-8 as U+FFFD (3 bytes), inflating adversarial input past the cap.
func sanitizeLastError(s string) string {
	s = truncateAtRuneBoundary(s, maxLastErrorBytes)
	out := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return truncateAtRuneBoundary(out, maxLastErrorBytes)
}

func truncateAtRuneBoundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// MarkSent transitions a claimed row to status='sent'. Caller invokes
// after sink.Send returns nil.
func MarkSent(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx, `
		UPDATE pgrelay_outbox
		SET status = 'sent',
		    sent_at = now(),
		    leased_until = NULL,
		    last_error = NULL
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("mark sent %d: %w", id, err)
	}
	return nil
}

// MarkRetry returns a claimed row to status='pending' with a future
// next_attempt_at (computed by the caller's backoff policy) and records
// the last error so operators can diagnose without grepping logs.
func MarkRetry(ctx context.Context, pool *pgxpool.Pool, id int64, nextAttemptAt time.Time, lastError string) error {
	_, err := pool.Exec(ctx, `
		UPDATE pgrelay_outbox
		SET status = 'pending',
		    next_attempt_at = $2,
		    last_error = $3,
		    leased_until = NULL
		WHERE id = $1
	`, id, nextAttemptAt, sanitizeLastError(lastError))
	if err != nil {
		return fmt.Errorf("mark retry %d: %w", id, err)
	}
	return nil
}

// MarkDead permanently transitions a row to status='dead'. Caller
// invokes on terminal sink errors (4xx) or when attempts exceeds
// MaxAttempts. Dead rows are not redispatched.
func MarkDead(ctx context.Context, pool *pgxpool.Pool, id int64, lastError string) error {
	_, err := pool.Exec(ctx, `
		UPDATE pgrelay_outbox
		SET status = 'dead',
		    last_error = $2,
		    leased_until = NULL
		WHERE id = $1
	`, id, sanitizeLastError(lastError))
	if err != nil {
		return fmt.Errorf("mark dead %d: %w", id, err)
	}
	return nil
}
