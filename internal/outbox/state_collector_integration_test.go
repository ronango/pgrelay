//go:build integration

package outbox_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/ronango/pgrelay/internal/outbox"
	"github.com/ronango/pgrelay/internal/testdb"
)

func TestStateCollector_EmptyTable(t *testing.T) {
	pool := testdb.New(t)
	reg := prometheus.NewRegistry()
	outbox.RegisterStateCollector(reg, pool, quietLogger())

	if got := collectGauge(t, reg, "pgrelay_outbox_backlog_seconds", nil); got != 0 {
		t.Errorf("backlog = %v, want 0", got)
	}
	for _, status := range []string{"pending", "in_flight"} {
		if got := collectGauge(t, reg, "pgrelay_outbox_rows", map[string]string{"status": status}); got != 0 {
			t.Errorf("rows{status=%q} = %v, want 0", status, got)
		}
	}
}

func TestStateCollector_CountsAndBacklog(t *testing.T) {
	pool := testdb.New(t)

	// Oldest pending row anchored 10 minutes in the past so backlog
	// assertion has comfortable headroom over container clock jitter.
	insertRow(t, pool, insertOpts{Status: "pending", NextAttemptAt: time.Now().Add(-10 * time.Minute)})
	insertRow(t, pool, insertOpts{Status: "pending", NextAttemptAt: time.Now().Add(-5 * time.Second)})
	insertRow(t, pool, insertOpts{Status: "pending", NextAttemptAt: time.Now().Add(time.Hour)})

	insertRow(t, pool, insertOpts{
		Status: "in_flight", Attempts: 1,
		LeasedUntil: time.Now().Add(time.Minute),
	})
	insertRow(t, pool, insertOpts{Status: "sent"})
	insertRow(t, pool, insertOpts{Status: "dead"})

	reg := prometheus.NewRegistry()
	outbox.RegisterStateCollector(reg, pool, quietLogger())

	if got := collectGauge(t, reg, "pgrelay_outbox_rows", map[string]string{"status": "pending"}); got != 3 {
		t.Errorf("rows{pending} = %v, want 3", got)
	}
	if got := collectGauge(t, reg, "pgrelay_outbox_rows", map[string]string{"status": "in_flight"}); got != 1 {
		t.Errorf("rows{in_flight} = %v, want 1", got)
	}

	if got := collectGauge(t, reg, "pgrelay_outbox_backlog_seconds", nil); got < 60 {
		t.Errorf("backlog = %v, want >= 60s (oldest pending was 10m ago)", got)
	}
}

// collectGauge fails the test if the metric is absent.
func collectGauge(t testing.TB, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if matchLabels(m.GetLabel(), labels) {
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %q with labels %v not found", name, labels)
	return 0
}

func matchLabels(got []*dto.LabelPair, want map[string]string) bool {
	if len(want) == 0 {
		return len(got) == 0
	}
	if len(got) != len(want) {
		return false
	}
	for _, l := range got {
		if want[l.GetName()] != l.GetValue() {
			return false
		}
	}
	return true
}
