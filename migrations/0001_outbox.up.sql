-- pgrelay_outbox is the transactional outbox table. Producers INSERT into
-- it inside the same transaction as their business write; the dispatcher
-- (#6) polls it via FOR UPDATE SKIP LOCKED.
--
-- ORDERING CAVEAT: BIGSERIAL is monotonic per-connection but NOT per-commit.
-- Two concurrent producers writing to the same aggregate can commit out of
-- id order; readers ORDER BY id may then dispatch the later-committed row
-- first. This is acceptable for v0.1.0-alpha (per-aggregate ordering is
-- post-Week-2 per the plan). Resolve before per-aggregate ordering ships:
-- typical fix is a (aggregate_id, seq BIGINT) populated under a transactional
-- advisory lock, or an xact_id snapshot via pg_current_xact_id() (PG 13+).
--
-- DEFERRED: a standalone (status) index for ad-hoc operator triage queries.
-- Outbox is a work queue with retention, not a log table — partial indexes
-- below cover hot paths. Add (status) index only if/when retention slips
-- and `WHERE status='dead'` queries become observably slow.
CREATE TABLE pgrelay_outbox (
    id              BIGSERIAL PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    headers         JSONB,
    traceparent     TEXT,
    tracestate      TEXT,
    sink            TEXT        NOT NULL
                                CHECK (sink IN ('http', 'kafka')),
    destination     TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'in_flight', 'sent', 'dead')),
    attempts        INT         NOT NULL DEFAULT 0
                                CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_until    TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ
);

-- Dispatcher claim hot path: SELECT ... WHERE status = 'pending' AND
-- next_attempt_at <= now() FOR UPDATE SKIP LOCKED. Predicate uses status
-- literal so the planner narrows to this index.
CREATE INDEX idx_pgrelay_outbox_pending_due
    ON pgrelay_outbox (next_attempt_at)
    WHERE status = 'pending';

-- Lease reaper sweep: SELECT ... WHERE status = 'in_flight' AND
-- leased_until < now() — used by the orphan-claim reclaim goroutine in #6.
CREATE INDEX idx_pgrelay_outbox_in_flight_lease
    ON pgrelay_outbox (leased_until)
    WHERE status = 'in_flight';

-- Per-aggregate ordering support (used when ORDER BY aggregate_id, id is set).
-- See ORDERING CAVEAT above before relying on this for strict ordering.
CREATE INDEX idx_pgrelay_outbox_aggregate
    ON pgrelay_outbox (aggregate_id, id)
    WHERE status = 'pending';
