// Package pgconn builds and instruments the pgx/v5 connection pool used
// across pgrelay. It exposes a New() factory with Prometheus-instrumented
// query/batch tracers and a pool-stats collector.
package pgconn

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// ctxKey is the unexported context-key type used by tracer to pass the
// operation start time from TraceXxxStart to TraceXxxEnd. Struct-typed so
// it cannot collide with int-keyed values in the same context chain.
type ctxKey struct{}

var startTimeKey = ctxKey{}

// tracer implements pgx.QueryTracer and pgx.BatchTracer, recording each
// operation's duration and error count to Prometheus metrics keyed by op
// (`query` for Query/QueryRow/Exec, `batch` for SendBatch).
type tracer struct {
	duration *prometheus.HistogramVec
	errors   *prometheus.CounterVec
}

// queryDurationBuckets covers a typical OLTP query latency range
// (1ms .. 5s) at single-digit-bucket granularity per decade.
var queryDurationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}

// newTracer registers the two metrics on reg and returns the tracer.
// Panics on duplicate registration; New() handles cleanup on its own
// failure paths so a failed New() does not leave the registry poisoned.
func newTracer(reg prometheus.Registerer) *tracer {
	t := &tracer{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pgrelay_db_query_duration_seconds",
			Help:    "Database operation duration in seconds, observed at end of operation.",
			Buckets: queryDurationBuckets,
		}, []string{"op"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pgrelay_db_query_errors_total",
			Help: "Total number of database operations that returned an error.",
		}, []string{"op"}),
	}
	reg.MustRegister(t.duration, t.errors)
	return t
}

func (t *tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, startTimeKey, time.Now())
}

func (t *tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	t.observe(ctx, "query", data.Err)
}

func (t *tracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return context.WithValue(ctx, startTimeKey, time.Now())
}

// TraceBatchQuery is required by pgx.BatchTracer to satisfy the interface
// even though we don't observe per-query timing inside a batch — the batch
// as a whole is timed via Start/End. Without this method the interface
// assertion fails and pgx skips batch tracing entirely.
func (t *tracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, _ pgx.TraceBatchQueryData) {
}

func (t *tracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	t.observe(ctx, "batch", data.Err)
}

func (t *tracer) observe(ctx context.Context, op string, err error) {
	if start, ok := ctx.Value(startTimeKey).(time.Time); ok {
		t.duration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	}
	if err != nil {
		t.errors.WithLabelValues(op).Inc()
	}
}
