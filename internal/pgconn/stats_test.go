package pgconn

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// TestPoolStatsCollector_ReportsZeroOnFreshPool exercises Describe and
// Collect end-to-end via a registry. pgxpool creates the pool object
// lazily, so Stat() returns zeros without a reachable database.
func TestPoolStatsCollector_ReportsZeroOnFreshPool(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	reg.MustRegister(newPoolStatsCollector(pool))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	want := map[string]float64{
		"pgrelay_db_pool_acquired": 0,
		"pgrelay_db_pool_idle":     0,
		"pgrelay_db_pool_total":    0,
	}
	for _, mf := range mfs {
		v, ok := want[mf.GetName()]
		if !ok {
			continue
		}
		got := mf.GetMetric()[0].GetGauge().GetValue()
		if got != v {
			t.Errorf("%s = %v, want %v", mf.GetName(), got, v)
		}
		delete(want, mf.GetName())
	}
	if len(want) > 0 {
		t.Errorf("missing gauges from registry: %v", want)
	}
}
