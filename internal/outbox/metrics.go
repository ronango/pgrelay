package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the dispatcher's Prometheus surface for eagerly-tracked
// signals; live gauges are in stateCollector.
type Metrics struct {
	// Retries increment Attempts too — sum across labels is total
	// attempts, not unique rows.
	Attempts         *prometheus.CounterVec
	DispatchDuration *prometheus.HistogramVec
	OrphansReclaimed prometheus.Counter
}

// dispatchDurationBuckets: 5ms..30s covers typical webhook RTTs and
// puts the top bucket above any plausible HTTPSink Timeout, so a
// stuck sink shows as +Inf rather than getting lost in the tail.
var dispatchDurationBuckets = []float64{0.005, 0.025, 0.1, 0.5, 1, 5, 15, 30}

// NewMetrics panics on duplicate registration; the process owns one
// Registerer, so duplicates indicate a programming bug at boot.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pgrelay_outbox_attempts_total",
			Help: "Total dispatch attempts, labeled by outcome (sent, retry, dead). Sum across labels is total attempts including retries.",
		}, []string{"result"}),
		DispatchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pgrelay_outbox_dispatch_duration_seconds",
			Help:    "Wall-clock time spent in sink.Send per row, labeled by sink.",
			Buckets: dispatchDurationBuckets,
		}, []string{"sink"}),
		OrphansReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pgrelay_outbox_orphans_reclaimed_total",
			Help: "Total in_flight rows the lease sweeper returned to pending.",
		}),
	}
	reg.MustRegister(m.Attempts, m.DispatchDuration, m.OrphansReclaimed)
	return m
}

// stateCollector samples the outbox table once per scrape under a
// bounded timeout so a slow DB can't wedge the /metrics endpoint.
type stateCollector struct {
	pool    *pgxpool.Pool
	timeout time.Duration
	log     *slog.Logger

	backlog *prometheus.Desc
	rows    *prometheus.Desc
}

// stateCollectorTimeout leaves headroom under the default 10s
// Prometheus scrape timeout so other collectors still get a chance.
const stateCollectorTimeout = 3 * time.Second

// RegisterStateCollector adds gauges sampled on each scrape.
func RegisterStateCollector(reg prometheus.Registerer, pool *pgxpool.Pool, log *slog.Logger) prometheus.Collector {
	c := &stateCollector{
		pool:    pool,
		timeout: stateCollectorTimeout,
		log:     log,
		backlog: prometheus.NewDesc(
			"pgrelay_outbox_backlog_seconds",
			"Age of the oldest pending row past its next_attempt_at. 0 when nothing is due.",
			nil, nil,
		),
		rows: prometheus.NewDesc(
			"pgrelay_outbox_rows",
			"Live row counts by status. 'dead' is omitted — query the table directly for triage.",
			[]string{"status"}, nil,
		),
	}
	reg.MustRegister(c)
	return c
}

func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.backlog
	ch <- c.rows
}

func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	// FILTER aggregates run over the partial indexes for both statuses;
	// the planner typically combines them via BitmapOr.
	const sql = `
		SELECT
			COALESCE(EXTRACT(EPOCH FROM (now() - MIN(next_attempt_at) FILTER (WHERE status = 'pending' AND next_attempt_at <= now()))), 0)::float8 AS backlog,
			COUNT(*) FILTER (WHERE status = 'pending')   AS pending,
			COUNT(*) FILTER (WHERE status = 'in_flight') AS in_flight
		FROM pgrelay_outbox
		WHERE status IN ('pending', 'in_flight')
	`

	var backlog float64
	var pending, inFlight int64
	if err := c.pool.QueryRow(ctx, sql).Scan(&backlog, &pending, &inFlight); err != nil {
		// No-emit on failure: Prometheus surfaces absence as stale,
		// which is more informative than a fabricated zero.
		c.log.WarnContext(ctx, "outbox state collector query failed", "err", err)
		return
	}

	ch <- prometheus.MustNewConstMetric(c.backlog, prometheus.GaugeValue, backlog)
	ch <- prometheus.MustNewConstMetric(c.rows, prometheus.GaugeValue, float64(pending), "pending")
	ch <- prometheus.MustNewConstMetric(c.rows, prometheus.GaugeValue, float64(inFlight), "in_flight")
}

// Result labels for Metrics.Attempts.
const (
	ResultSent  = "sent"
	ResultRetry = "retry"
	ResultDead  = "dead"
)
