package outbox_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ronango/pgrelay/internal/outbox"
)

func TestNewMetrics_RegistersExpectedMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := outbox.NewMetrics(reg)

	m.Attempts.WithLabelValues(outbox.ResultSent).Inc()
	m.Attempts.WithLabelValues(outbox.ResultRetry).Inc()
	m.Attempts.WithLabelValues(outbox.ResultDead).Inc()
	m.DispatchDuration.WithLabelValues("http").Observe(0.1)
	m.OrphansReclaimed.Inc()

	// Histogram has one labeled series after the Observe above.
	if got := testutil.CollectAndCount(m.DispatchDuration); got != 1 {
		t.Errorf("DispatchDuration series count = %d, want 1", got)
	}

	want := `
# HELP pgrelay_outbox_attempts_total Total dispatch attempts, labeled by outcome (sent, retry, dead). Sum across labels is total attempts including retries.
# TYPE pgrelay_outbox_attempts_total counter
pgrelay_outbox_attempts_total{result="dead"} 1
pgrelay_outbox_attempts_total{result="retry"} 1
pgrelay_outbox_attempts_total{result="sent"} 1
# HELP pgrelay_outbox_orphans_reclaimed_total Total in_flight rows the lease sweeper returned to pending.
# TYPE pgrelay_outbox_orphans_reclaimed_total counter
pgrelay_outbox_orphans_reclaimed_total 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"pgrelay_outbox_attempts_total",
		"pgrelay_outbox_orphans_reclaimed_total",
	); err != nil {
		t.Errorf("metrics mismatch: %v", err)
	}
}

func TestNewMetrics_DuplicateRegistrationPanics(t *testing.T) {
	reg := prometheus.NewRegistry()
	outbox.NewMetrics(reg)

	defer func() {
		if recover() == nil {
			t.Error("second NewMetrics on same registry should have panicked")
		}
	}()
	outbox.NewMetrics(reg)
}
