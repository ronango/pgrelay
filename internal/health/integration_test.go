//go:build integration

// This file lives under internal/health because health is the package
// that orchestrates otel + metrics + health into a single ops surface.
// Test name carries the broader Observability prefix to advertise scope.

package health_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/ronango/pgrelay/internal/health"
	"github.com/ronango/pgrelay/internal/metrics"
	"github.com/ronango/pgrelay/internal/otel"
)

// TestObservability_FullStack wires otel + metrics + health together,
// drives every observability surface in one go, and asserts:
//
//   - /healthz, /readyz, /metrics work end-to-end.
//   - /readyz reflects probe state changes (503 → 200 on toggle).
//   - /metrics exposes the Go runtime collectors registered by metrics.NewRegistry.
//   - The W3C propagator installed by otel.Setup round-trips a span context.
//   - Shutdown drains the BatchSpanProcessor: a fake OTLP/HTTP collector
//     receives an OTLP-typed POST after End() + Shutdown().
func TestObservability_FullStack(t *testing.T) {
	httpClient := &http.Client{Timeout: 2 * time.Second}

	// 1. Fake OTLP/HTTP collector — counts POSTs and records Content-Type
	//    of the first one so we can assert it's actually OTLP traffic.
	var (
		exported    atomic.Int64
		contentType atomic.Value
	)
	collectorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exported.Add(1) == 1 {
			contentType.Store(r.Header.Get("Content-Type"))
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collectorSrv.Close)

	// 2. Snapshot/restore global OTel state for hermetic execution.
	prevProp := otelapi.GetTextMapPropagator()
	prevTP := otelapi.GetTracerProvider()
	t.Cleanup(func() {
		otelapi.SetTextMapPropagator(prevProp)
		otelapi.SetTracerProvider(prevTP)
	})

	shutdownOtel, err := otel.Setup(t.Context(), otel.Config{
		ServiceName:    "pgrelay-itest",
		ServiceVersion: "0.0.0-test",
		OTLPEndpoint:   collectorSrv.URL,
		TracesSampler:  "always_on",
	})
	if err != nil {
		t.Fatalf("otel.Setup: %v", err)
	}
	// Register shutdown cleanup immediately so the batch processor is
	// drained even on a t.Fatal further down.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownOtel(ctx)
	})

	// 3. Metrics registry + toggleable readiness probe.
	reg := metrics.NewRegistry()
	var ready atomic.Bool
	probe := health.ProbeFunc(func(context.Context) error {
		if !ready.Load() {
			return errors.New("warming up")
		}
		return nil
	})

	// 4. Health server on an ephemeral port.
	srv := health.New("127.0.0.1:0", reg, probe)
	srvCtx, srvCancel := context.WithCancel(t.Context())
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Run(srvCtx) }()

	addrCtx, addrCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer addrCancel()
	addr, err := srv.Addr(addrCtx)
	if err != nil {
		t.Fatalf("waiting for bind: %v", err)
	}
	base := "http://" + addr

	// 5. /healthz: liveness 200 always.
	assertStatus(t, httpClient, base+"/healthz", http.StatusOK)

	// 6. /readyz: 503 → toggle ready → 200.
	assertStatus(t, httpClient, base+"/readyz", http.StatusServiceUnavailable)
	ready.Store(true)
	assertStatus(t, httpClient, base+"/readyz", http.StatusOK)

	// 7. /metrics: includes runtime + build info.
	body := getBody(t, httpClient, base+"/metrics")
	for _, want := range []string{"# HELP go_", "go_build_info"} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
			t.Logf("/metrics body:\n%s", body)
			break
		}
	}

	// 8. Propagator round-trip across a sampled span.
	tracer := otelapi.Tracer("pgrelay-itest")
	ctx, span := tracer.Start(t.Context(), "integration-span")
	originalSC := span.SpanContext()
	if !originalSC.IsValid() {
		t.Fatal("span has invalid SpanContext (sampler must emit IDs)")
	}

	carrier := propagation.MapCarrier{}
	otelapi.GetTextMapPropagator().Inject(ctx, carrier)
	if carrier.Get("traceparent") == "" {
		t.Error("propagator did not inject traceparent")
	}
	extractedSC := trace.SpanContextFromContext(
		otelapi.GetTextMapPropagator().Extract(context.Background(), carrier),
	)
	if extractedSC.TraceID() != originalSC.TraceID() {
		t.Errorf("TraceID mismatch after round-trip: got %s, want %s", extractedSC.TraceID(), originalSC.TraceID())
	}

	span.End()

	// 9. Shutdown OTel: BatchSpanProcessor must drain to the fake collector.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := shutdownOtel(shutdownCtx); err != nil {
		t.Errorf("otel shutdown: %v", err)
	}
	if got := exported.Load(); got < 1 {
		t.Errorf("fake OTLP collector received %d exports, want >= 1 (drain failed)", got)
	}
	if ct, _ := contentType.Load().(string); ct != "" && !strings.HasPrefix(ct, "application/x-protobuf") {
		t.Errorf("first export Content-Type = %q, want application/x-protobuf*", ct)
	}

	// 10. Shutdown health server gracefully (cancel early so we can
	//     assert Run returned nil before t ends).
	srvCancel()
	select {
	case err := <-srvDone:
		if err != nil {
			t.Errorf("health server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health server did not stop within 2s")
	}
}

// assertStatus issues a GET via the timeout-bounded client and asserts
// the response status. Body is logged on mismatch for debug.
func assertStatus(t *testing.T, c *http.Client, url string, want int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		body, _ := io.ReadAll(res.Body)
		t.Errorf("GET %s status = %d, want %d (body: %s)", url, res.StatusCode, want, body)
	}
}

func getBody(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", url, res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return string(body)
}
