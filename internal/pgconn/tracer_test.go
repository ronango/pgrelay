package pgconn

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestTracer_QueryRecordsDuration(t *testing.T) {
	tr := newTracer(prometheus.NewRegistry())

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got := histogramSampleCount(t, tr.duration.WithLabelValues("query")); got != 1 {
		t.Errorf("duration[query] sample count = %d, want 1", got)
	}
	if got := testutil.ToFloat64(tr.errors.WithLabelValues("query")); got != 0 {
		t.Errorf("errors[query] = %v, want 0", got)
	}
}

func histogramSampleCount(t *testing.T, h prometheus.Observer) uint64 {
	t.Helper()
	m := &dto.Metric{}
	if err := h.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("histogram write: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestTracer_QueryErrorIncrementsCounter(t *testing.T) {
	tr := newTracer(prometheus.NewRegistry())

	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("boom")})

	if got := testutil.ToFloat64(tr.errors.WithLabelValues("query")); got != 1 {
		t.Errorf("errors[query] = %v, want 1", got)
	}
}

func TestTracer_BatchUsesBatchLabel(t *testing.T) {
	tr := newTracer(prometheus.NewRegistry())

	ctx := tr.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{})
	tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: errors.New("boom")})

	if got := testutil.ToFloat64(tr.errors.WithLabelValues("batch")); got != 1 {
		t.Errorf("errors[batch] = %v, want 1", got)
	}
	if got := testutil.ToFloat64(tr.errors.WithLabelValues("query")); got != 0 {
		t.Errorf("errors[query] = %v, want 0 (batch op should not bleed into query label)", got)
	}
}

func TestTracer_EndWithoutStartIsSafe(t *testing.T) {
	// Defensive: if a caller invokes TraceQueryEnd with a context that
	// never went through TraceQueryStart (test mistake, refactor regression),
	// no panic and no histogram observation.
	tr := newTracer(prometheus.NewRegistry())

	tr.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})

	if got := testutil.CollectAndCount(tr.duration); got != 0 {
		t.Errorf("duration series count = %d, want 0 (no start = no observation)", got)
	}
}
