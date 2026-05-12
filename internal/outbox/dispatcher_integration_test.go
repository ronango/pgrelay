//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ronango/pgrelay/internal/config"
	"github.com/ronango/pgrelay/internal/outbox"
	"github.com/ronango/pgrelay/internal/outbox/sinks"
	"github.com/ronango/pgrelay/internal/outbox/sinks/sinkmock"
	"github.com/ronango/pgrelay/internal/testdb"
)

// dispatcherFixture starts Run() and registers cancel/wait via t.Cleanup.
type dispatcherFixture struct {
	Pool    *pgxpool.Pool
	Sink    *sinkmock.Sink
	Metrics *outbox.Metrics
	Reg     *prometheus.Registry
	Cfg     config.Config

	cancel context.CancelFunc
	done   chan error
}

// fastConfig is the dispatcher tuning used by these tests — short
// intervals so a test can observe a full poll+sweep cycle in <500ms.
func fastConfig() config.Config {
	return config.Config{
		PollInterval:  10 * time.Millisecond,
		BatchSize:     32,
		LeaseDuration: 500 * time.Millisecond,
		MaxAttempts:   3,
		RetryBase:     10 * time.Millisecond,
		RetryMax:      50 * time.Millisecond,
		RetryJitter:   0,
	}
}

func startDispatcher(t *testing.T, pool *pgxpool.Pool, cfg config.Config) *dispatcherFixture {
	t.Helper()

	sink := sinkmock.New(sinks.SinkHTTP)
	policy := outbox.NewPolicy(cfg)
	reg := prometheus.NewRegistry()
	metrics := outbox.NewMetrics(reg)
	d := outbox.New(pool, sink, policy, metrics, cfg, quietLogger())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	f := &dispatcherFixture{
		Pool: pool, Sink: sink, Metrics: metrics, Reg: reg, Cfg: cfg,
		cancel: cancel, done: done,
	}
	t.Cleanup(f.Stop)
	return f
}

// attemptsCount returns the Attempts counter for a given result label.
func (f *dispatcherFixture) attemptsCount(result string) float64 {
	return testutil.ToFloat64(f.Metrics.Attempts.WithLabelValues(result))
}

// Stop cancels the dispatcher and waits up to 2s for Run to return.
// Idempotent — second call sees a closed channel and returns immediately.
func (f *dispatcherFixture) Stop() {
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
	}
}

func TestDispatcher_HappyPath(t *testing.T) {
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	id := insertRow(t, pool, insertOpts{})

	r := waitForStatus(t, pool, id, "sent", 2*time.Second)
	if r.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", r.Attempts)
	}
	if r.LeasedUntil != nil {
		t.Errorf("LeasedUntil = %v, want NULL after sent", r.LeasedUntil)
	}
	if f.Sink.SentCount() == 0 {
		t.Error("sink received no messages")
	}
	if got := f.attemptsCount(outbox.ResultSent); got < 1 {
		t.Errorf("attempts_total{sent} = %v, want >= 1", got)
	}
}

func TestDispatcher_OversizedPayloadMarkedDeadWithoutSending(t *testing.T) {
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	// 1 MiB + 1 byte — over the dispatcher cap.
	oversized := make([]byte, (1<<20)+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	// Wrap as JSON string so the JSONB column accepts it.
	payload := append([]byte{'"'}, oversized...)
	payload = append(payload, '"')

	id := insertRow(t, pool, insertOpts{Payload: payload})

	r := waitForStatus(t, pool, id, "dead", 2*time.Second)
	if !strings.Contains(r.LastError, "exceeds") {
		t.Errorf("LastError = %q, want mention of cap exceeded", r.LastError)
	}
	if f.Sink.SentCount() != 0 {
		t.Errorf("sink saw %d messages, want 0 (oversized never sent)", f.Sink.SentCount())
	}
	if got := f.attemptsCount(outbox.ResultDead); got < 1 {
		t.Errorf("attempts_total{dead} = %v, want >= 1", got)
	}
}

func TestDispatcher_NonRetryableMarksDead(t *testing.T) {
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	f.Sink.EnqueueErrors(errors.New("400 Bad Request"))
	id := insertRow(t, pool, insertOpts{})

	r := waitForStatus(t, pool, id, "dead", 2*time.Second)
	if r.LastError == "" {
		t.Error("LastError empty, want sink error text")
	}
	if got := f.attemptsCount(outbox.ResultDead); got < 1 {
		t.Errorf("attempts_total{dead} = %v, want >= 1", got)
	}
}

func TestDispatcher_RetryableReschedules(t *testing.T) {
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	f.Sink.EnqueueErrors(sinks.NewRetryableError(errors.New("503"), 0), nil)
	id := insertRow(t, pool, insertOpts{})

	r := waitForStatus(t, pool, id, "sent", 3*time.Second)
	if r.Attempts < 2 {
		t.Errorf("Attempts = %d, want >= 2 (one retry + one success)", r.Attempts)
	}
	if f.Sink.SentCount() < 2 {
		t.Errorf("sink saw %d messages, want >= 2", f.Sink.SentCount())
	}
	if got := f.attemptsCount(outbox.ResultRetry); got < 1 {
		t.Errorf("attempts_total{retry} = %v, want >= 1", got)
	}
	if got := f.attemptsCount(outbox.ResultSent); got < 1 {
		t.Errorf("attempts_total{sent} = %v, want >= 1", got)
	}
}

func TestDispatcher_MaxAttemptsExhaustedGoesDead(t *testing.T) {
	pool := testdb.New(t)
	cfg := fastConfig()
	cfg.MaxAttempts = 2
	f := startDispatcher(t, pool, cfg)

	// Always retryable — dispatcher should give up after MaxAttempts.
	f.Sink.SetOnSend(func(_ context.Context, _ sinks.Message) error {
		return sinks.NewRetryableError(errors.New("503"), 0)
	})

	id := insertRow(t, pool, insertOpts{})
	r := waitForStatus(t, pool, id, "dead", 3*time.Second)
	if r.Attempts < cfg.MaxAttempts {
		t.Errorf("Attempts = %d, want >= %d", r.Attempts, cfg.MaxAttempts)
	}
}

func TestDispatcher_RetryAfterIsFloor(t *testing.T) {
	pool := testdb.New(t)
	cfg := fastConfig()
	// Make policy's NextDelay tiny so RetryAfter clearly wins.
	cfg.RetryBase = time.Millisecond
	cfg.RetryMax = 5 * time.Second
	f := startDispatcher(t, pool, cfg)

	const retryAfter = 800 * time.Millisecond
	var sent atomic.Int32
	f.Sink.SetOnSend(func(_ context.Context, _ sinks.Message) error {
		if sent.Add(1) == 1 {
			return sinks.NewRetryableError(errors.New("429 slow down"), retryAfter)
		}
		return nil
	})

	id := insertRow(t, pool, insertOpts{})

	// First attempt fails; row goes back to pending with next_attempt_at
	// far enough in the future that the dispatcher cannot have retried
	// yet — exactly the floor we want to verify.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r := getRow(t, pool, id)
		if r.Status == "pending" && r.Attempts == 1 {
			gap := time.Until(r.NextAttemptAt)
			// Allow some slack for ticker latency.
			if gap < retryAfter-100*time.Millisecond {
				t.Errorf("NextAttemptAt gap = %s, want ≈ %s (RetryAfter floor)", gap, retryAfter)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never observed retry state for row %d", id)
}

func TestDispatcher_SweepsOrphans(t *testing.T) {
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	// Pre-orphaned: in_flight with lease already expired. Sweeper
	// returns it to pending, then dispatcher re-delivers.
	id := insertRow(t, pool, insertOpts{
		Status:      "in_flight",
		Attempts:    1,
		LeasedUntil: time.Now().Add(-time.Second),
	})

	r := waitForStatus(t, pool, id, "sent", 3*time.Second)
	if r.LastError != "" {
		t.Errorf("LastError = %q, want cleared by MarkSent after redelivery", r.LastError)
	}
	if f.Sink.SentCount() == 0 {
		t.Error("orphan never re-dispatched")
	}
	if got := testutil.ToFloat64(f.Metrics.OrphansReclaimed); got < 1 {
		t.Errorf("orphans_reclaimed_total = %v, want >= 1", got)
	}
}

func TestDispatcher_ShutdownLeavesInFlightForSweeper(t *testing.T) {
	// At-least-once invariant: when Send returns ctx.Canceled (or
	// DeadlineExceeded), we cannot tell whether the request reached
	// the sink. Marking the row dead would lose redelivery; the lease
	// sweeper must own recovery.
	pool := testdb.New(t)
	f := startDispatcher(t, pool, fastConfig())

	released := make(chan struct{})
	f.Sink.SetOnSend(func(ctx context.Context, _ sinks.Message) error {
		<-ctx.Done()
		close(released)
		return ctx.Err()
	})

	id := insertRow(t, pool, insertOpts{})
	waitForStatus(t, pool, id, "in_flight", 2*time.Second)

	f.Stop()
	<-released

	r := getRow(t, pool, id)
	if r.Status != "in_flight" {
		t.Errorf("post-shutdown status = %q, want in_flight", r.Status)
	}
	if r.LastError != "" {
		t.Errorf("LastError = %q, want empty (no terminal write)", r.LastError)
	}
}
