package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ronango/pgrelay/internal/config"
	"github.com/ronango/pgrelay/internal/outbox/sinks"
)

// Dispatcher is the long-running outbox worker. One Run() per
// Dispatcher; concurrent Runs would race the sink. Per-row
// concurrency within a batch is deferred to v0.2.
type Dispatcher struct {
	pool    *pgxpool.Pool
	sink    sinks.Sink
	policy  *Policy
	metrics *Metrics
	cfg     config.Config
	log     *slog.Logger
}

// finalizeTimeout caps the detached terminal-write so a canceled
// dispatch still persists its outcome instead of leaking to the sweeper.
const finalizeTimeout = 5 * time.Second

// New wires a Dispatcher.
func New(pool *pgxpool.Pool, sink sinks.Sink, policy *Policy, metrics *Metrics, cfg config.Config, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		pool:    pool,
		sink:    sink,
		policy:  policy,
		metrics: metrics,
		cfg:     cfg,
		log:     log,
	}
}

// Run blocks until ctx is canceled, then returns ctx.Err().
func (d *Dispatcher) Run(ctx context.Context) error {
	sweep := d.cfg.LeaseDuration / 2
	pollTicker := time.NewTicker(d.cfg.PollInterval)
	defer pollTicker.Stop()
	sweepTicker := time.NewTicker(sweep)
	defer sweepTicker.Stop()

	d.log.InfoContext(ctx, "dispatcher started",
		"sink", d.sink.Name(),
		"poll", d.cfg.PollInterval,
		"sweep", sweep,
		"batch", d.cfg.BatchSize,
		"lease", d.cfg.LeaseDuration,
		"max_attempts", d.policy.MaxAttempts,
	)

	for {
		select {
		case <-ctx.Done():
			d.log.InfoContext(ctx, "dispatcher stopping", "reason", ctx.Err())
			return ctx.Err()
		case <-pollTicker.C:
			d.poll(ctx)
		case <-sweepTicker.C:
			d.sweep(ctx)
		}
	}
}

func (d *Dispatcher) poll(ctx context.Context) {
	rows, err := Claim(ctx, d.pool, d.cfg.BatchSize, d.cfg.LeaseDuration)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			d.log.WarnContext(ctx, "claim failed", "err", err)
		}
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		d.dispatch(ctx, row)
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, row Row) {
	msg := sinks.Message{
		ID:            row.ID,
		AggregateType: row.AggregateType,
		AggregateID:   row.AggregateID,
		EventType:     row.EventType,
		Destination:   row.Destination,
		Payload:       row.Payload,
		Headers:       row.Headers,
		Traceparent:   row.Traceparent,
		Tracestate:    row.Tracestate,
	}

	start := time.Now()
	sendErr := d.sink.Send(ctx, msg)
	// Observed on every outcome so failure tails land in the histogram.
	d.metrics.DispatchDuration.WithLabelValues(d.sink.Name()).Observe(time.Since(start).Seconds())

	// Shutdown mid-Send: writing a terminal state would mask uncertainty
	// — the request may or may not have hit the sink. Leave the row
	// in_flight; the lease sweeper redelivers under at-least-once.
	if errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded) {
		return
	}

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	switch {
	case sendErr == nil:
		if err := MarkSent(writeCtx, d.pool, row.ID); err != nil {
			d.log.ErrorContext(writeCtx, "mark sent failed", "id", row.ID, "err", err)
			return
		}
		d.metrics.Attempts.WithLabelValues(ResultSent).Inc()
	case !sinks.IsRetryable(sendErr) || row.Attempts >= d.policy.MaxAttempts:
		errStr := sendErr.Error()
		if err := MarkDead(writeCtx, d.pool, row.ID, errStr); err != nil {
			d.log.ErrorContext(writeCtx, "mark dead failed", "id", row.ID, "err", err, "last_error", errStr)
			return
		}
		d.metrics.Attempts.WithLabelValues(ResultDead).Inc()
	default:
		d.scheduleRetry(writeCtx, row, sendErr)
	}
}

// scheduleRetry uses RetryAfter as a floor over policy.NextDelay,
// clamped to policy.Max so a misbehaving origin can't park a row
// past the operator-configured ceiling.
func (d *Dispatcher) scheduleRetry(ctx context.Context, row Row, sendErr error) {
	delay := d.policy.NextDelay(row.Attempts)
	if re, ok := sinks.AsRetryable(sendErr); ok {
		delay = min(max(delay, re.RetryAfter), d.policy.Max)
	}
	nextAt := time.Now().Add(delay)

	if err := MarkRetry(ctx, d.pool, row.ID, nextAt, sendErr.Error()); err != nil {
		d.log.ErrorContext(ctx, "mark retry failed", "id", row.ID, "err", err)
		return
	}
	d.metrics.Attempts.WithLabelValues(ResultRetry).Inc()
}

func (d *Dispatcher) sweep(ctx context.Context) {
	n, err := ReclaimOrphans(ctx, d.pool)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			d.log.WarnContext(ctx, "sweep failed", "err", err)
		}
		return
	}
	if n > 0 {
		d.metrics.OrphansReclaimed.Add(float64(n))
		d.log.InfoContext(ctx, "reclaimed orphaned rows", "count", n)
	}
}
