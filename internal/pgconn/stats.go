package pgconn

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// poolStatsCollector reports pgxpool connection counts to Prometheus.
// Implements prometheus.Collector so values are sampled live on every
// scrape via pool.Stat() — no background goroutine, no stale data.
type poolStatsCollector struct {
	pool *pgxpool.Pool

	acquired *prometheus.Desc
	idle     *prometheus.Desc
	total    *prometheus.Desc
}

func newPoolStatsCollector(pool *pgxpool.Pool) *poolStatsCollector {
	return &poolStatsCollector{
		pool: pool,
		acquired: prometheus.NewDesc(
			"pgrelay_db_pool_acquired",
			"Currently acquired connections in the pool.",
			nil, nil,
		),
		idle: prometheus.NewDesc(
			"pgrelay_db_pool_idle",
			"Currently idle (open but unused) connections in the pool.",
			nil, nil,
		),
		total: prometheus.NewDesc(
			"pgrelay_db_pool_total",
			"Total connections owned by the pool, including connections being constructed.",
			nil, nil,
		),
	}
}

// Note: unlike newTracer, this constructor does not self-register on a
// prometheus.Registerer — the collector has no internal vectors to wire
// up eagerly, so the caller registers it directly via reg.Register(...).

func (c *poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquired
	ch <- c.idle
	ch <- c.total
}

func (c *poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(s.TotalConns()))
}
