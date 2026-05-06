package health_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ronango/pgrelay/internal/health"
)

func alwaysReady() health.ProbeFunc { return func(context.Context) error { return nil } }
func neverReady() health.ProbeFunc {
	return func(context.Context) error { return errors.New("db down") }
}

func newTestServer(t *testing.T, probe health.ReadinessProbe) *httptest.Server {
	t.Helper()
	reg := prometheus.NewRegistry()
	// Register a tiny domain metric so /metrics has something pgrelay-shaped
	// to assert against (beyond the Go runtime collectors that aren't here).
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pgrelay_test_total",
		Help: "Test counter for /metrics smoke.",
	})
	reg.MustRegister(c)
	c.Inc()

	srv := health.New(":0", reg, probe)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthz_Always200(t *testing.T) {
	ts := newTestServer(t, neverReady()) // probe failure must not affect /healthz
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

func TestReadyz_OkWhenProbeReady(t *testing.T) {
	ts := newTestServer(t, alwaysReady())
	res, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

func TestReadyz_503WhenProbeFails(t *testing.T) {
	ts := newTestServer(t, neverReady())
	res, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "db down") {
		t.Errorf("body = %q, want it to surface the probe error", body)
	}
}

func TestMetrics_ServesPromFormat(t *testing.T) {
	ts := newTestServer(t, alwaysReady())
	res, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	text := string(body)
	if !strings.Contains(text, "# HELP pgrelay_test_total") {
		t.Errorf("expected HELP line for pgrelay_test_total in /metrics output, got:\n%s", text)
	}
	if !strings.Contains(text, "pgrelay_test_total 1") {
		t.Errorf("expected pgrelay_test_total 1 sample, got:\n%s", text)
	}
}

func TestNonGetRequestsRejected(t *testing.T) {
	ts := newTestServer(t, alwaysReady())
	cases := []string{"/healthz", "/readyz", "/metrics"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("POST %s status = %d, want 405", path, res.StatusCode)
			}
		})
	}
}

// TestRun_GracefulShutdownOnContextCancel boots the real Run() loop on
// an ephemeral port, waits for the listener via Addr(), cancels the
// context, and asserts Run returns nil (clean shutdown).
func TestRun_GracefulShutdownOnContextCancel(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := health.New("127.0.0.1:0", reg, alwaysReady())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Synchronize on the listener being bound — replaces the previous
	// flaky time.Sleep.
	addrCtx, addrCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer addrCancel()
	addr, err := srv.Addr(addrCtx)
	if err != nil {
		t.Fatalf("waiting for bind: %v", err)
	}
	if addr == "" {
		t.Fatal("Addr returned empty string after bind")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil after clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}

// TestRun_ListenFailureSurfacesViaAddr: when Run fails to bind (port
// already in use), Addr returns the listen error rather than blocking.
func TestRun_ListenFailureSurfacesViaAddr(t *testing.T) {
	// Bind a listener to occupy a port, then point Server at the same.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })

	reg := prometheus.NewRegistry()
	srv := health.New(occupied.Addr().String(), reg, alwaysReady())

	done := make(chan error, 1)
	go func() { done <- srv.Run(t.Context()) }()

	addrCtx, addrCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer addrCancel()
	if _, err := srv.Addr(addrCtx); err == nil {
		t.Error("Addr returned nil error despite occupied port")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned nil despite occupied port")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s on bind failure")
	}
}
