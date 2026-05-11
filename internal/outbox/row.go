// Package outbox is the dispatcher worker: claim pending rows under a
// lease, deliver via a sinks.Sink, mark sent / retry / dead, and reclaim
// orphaned in-flight rows when leases expire.
package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Row mirrors a pgrelay_outbox row. Nullable columns surface as zero
// values; the dispatcher only reads them when status guarantees presence
// (e.g. LeasedUntil after Claim, SentAt never read after MarkSent).
//
// Headers is map[string]string by contract with sinks.Message: producer
// writes single-valued headers to the outbox.headers JSONB object;
// outbound webhook delivery doesn't surface multi-valued forms.
type Row struct {
	ID            int64
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	Headers       map[string]string
	Traceparent   string
	Tracestate    string
	Sink          string
	Destination   string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LeasedUntil   time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        time.Time
}

// rowColumns is the SELECT/RETURNING column list shared by every query
// that produces a Row. Order matches scanRow.
const rowColumns = `
	id, aggregate_type, aggregate_id, event_type, payload, headers,
	traceparent, tracestate, sink, destination, status, attempts,
	next_attempt_at, leased_until, last_error, created_at, sent_at
`

func scanRow(rec pgx.CollectableRow) (Row, error) {
	var r Row
	var headersJSON []byte
	var traceparent, tracestate, lastError pgtype.Text
	var leasedUntil, sentAt pgtype.Timestamptz

	if err := rec.Scan(
		&r.ID, &r.AggregateType, &r.AggregateID, &r.EventType,
		&r.Payload, &headersJSON,
		&traceparent, &tracestate,
		&r.Sink, &r.Destination,
		&r.Status, &r.Attempts,
		&r.NextAttemptAt, &leasedUntil,
		&lastError,
		&r.CreatedAt, &sentAt,
	); err != nil {
		return Row{}, err
	}

	r.Traceparent = traceparent.String
	r.Tracestate = tracestate.String
	r.LastError = lastError.String
	if leasedUntil.Valid {
		r.LeasedUntil = leasedUntil.Time
	}
	if sentAt.Valid {
		r.SentAt = sentAt.Time
	}
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &r.Headers); err != nil {
			return Row{}, fmt.Errorf("unmarshal headers for row %d: %w", r.ID, err)
		}
	}
	return r, nil
}
