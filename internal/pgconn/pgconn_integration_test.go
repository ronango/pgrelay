//go:build integration

package pgconn_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/ronango/pgrelay/internal/pgconn"
	"github.com/ronango/pgrelay/internal/testdb"
)

// TestNew_AgainstTestdb is the round-trip integration check for pgconn.New:
// boots a real Postgres via testdb, builds a pool through pgconn.New,
// exercises query / batch / error paths plus pool acquire/release, then
// asserts the registry reflects every code path.
func TestNew_AgainstTestdb(t *testing.T) {
	// testdb.New registers t.Cleanup(pool.Close); we keep its pool alive
	// (idle) so we only borrow the DSN. No double-close.
	bootPool := testdb.New(t)
	dsn := bootPool.Config().ConnString()

	reg := prometheus.NewRegistry()
	pool, err := pgconn.New(t.Context(), pgconn.Config{
		DSN:               dsn,
		MinConns:          1,
		MaxConns:          5,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: time.Minute,
	}, reg)
	if err != nil {
		t.Fatalf("pgconn.New: %v", err)
	}
	defer pool.Close()

	// 1. Successful query — exercises QueryTracer happy path.
	var one int
	if err := pool.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 returned %d, want 1", one)
	}

	// 2. Failing query — exercises QueryTracer error counter path.
	if _, err := pool.Exec(t.Context(), "SELECT * FROM pgrelay_outbox_does_not_exist"); err == nil {
		t.Fatal("expected error for missing table, got nil")
	}

	// 3. Batch — exercises BatchTracer with op=batch label.
	batch := &pgx.Batch{}
	batch.Queue("SELECT 1")
	batch.Queue("SELECT 2")
	br := pool.SendBatch(t.Context(), batch)
	for range 2 {
		if _, err := br.Exec(); err != nil {
			t.Fatalf("batch exec: %v", err)
		}
	}
	if err := br.Close(); err != nil {
		t.Fatalf("batch close: %v", err)
	}

	// 4. Acquire/release — proves the pool gauge updates (not hard-coded).
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	mfsHeld := mustGather(t, reg)
	if got := gaugeValue(t, mfsHeld, "pgrelay_db_pool_acquired"); got < 1 {
		t.Errorf("pool_acquired while holding = %v, want >= 1", got)
	}
	conn.Release()

	mfs := mustGather(t, reg)

	// Tracer: query + error + batch all contributed series under their op.
	if got := histogramSampleCount(t, mfs, "pgrelay_db_query_duration_seconds", "op", "query"); got < 2 {
		t.Errorf("query histogram count = %d, want >= 2 (success + error)", got)
	}
	if got := histogramSampleCount(t, mfs, "pgrelay_db_query_duration_seconds", "op", "batch"); got < 1 {
		t.Errorf("batch histogram count = %d, want >= 1", got)
	}
	if got := counterValue(t, mfs, "pgrelay_db_query_errors_total", "op", "query"); got < 1 {
		t.Errorf("query errors counter = %v, want >= 1 (failed Exec)", got)
	}

	// Stats: all three gauges present, total >= 1.
	for _, name := range []string{"pgrelay_db_pool_acquired", "pgrelay_db_pool_idle", "pgrelay_db_pool_total"} {
		if !hasMetric(mfs, name) {
			t.Errorf("missing gauge %q in registry", name)
		}
	}
	if got := gaugeValue(t, mfs, "pgrelay_db_pool_total"); got < 1 {
		t.Errorf("pool_total = %v, want >= 1", got)
	}
}

// --- helpers ---

func mustGather(t *testing.T, reg *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	return mfs
}

func histogramSampleCount(t *testing.T, mfs []*dto.MetricFamily, name, label, value string) uint64 {
	t.Helper()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if hasLabel(m, label, value) {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	t.Fatalf("histogram %q with label %s=%s not found", name, label, value)
	return 0
}

func counterValue(t *testing.T, mfs []*dto.MetricFamily, name, label, value string) float64 {
	t.Helper()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if hasLabel(m, label, value) {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("counter %q with label %s=%s not found", name, label, value)
	return 0
}

func gaugeValue(t *testing.T, mfs []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		ms := mf.GetMetric()
		if len(ms) == 0 {
			t.Fatalf("gauge %q has no series", name)
		}
		return ms[0].GetGauge().GetValue()
	}
	t.Fatalf("gauge %q not found", name)
	return 0
}

func hasMetric(mfs []*dto.MetricFamily, name string) bool {
	for _, mf := range mfs {
		if mf.GetName() == name {
			return true
		}
	}
	return false
}

func hasLabel(m *dto.Metric, key, val string) bool {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == key && lp.GetValue() == val {
			return true
		}
	}
	return false
}
