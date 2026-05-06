// Package health serves the operational HTTP endpoints used by load
// balancers, orchestrators, and Prometheus scrapers:
//
//   - GET /healthz — liveness; returns 200 once the server is accepting.
//   - GET /readyz  — readiness; returns 200 when the configured probe is
//     ready, 503 otherwise.
//   - GET /metrics — Prometheus exposition for the supplied registry.
//
// The /readyz body surfaces the probe's error string verbatim — the
// endpoint is intended for trusted networks (intra-cluster scrapers,
// k8s probes) and should not be exposed externally.
//
// The server is wired into cmd/pgrelay run; tests can instantiate it
// directly and dispatch requests via Handler().
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ReadinessProbe reports whether the dispatcher is ready to serve work.
// A nil error means ready; any non-nil error fails /readyz.
type ReadinessProbe interface {
	Ready(ctx context.Context) error
}

// ProbeFunc adapts a function to the ReadinessProbe interface.
type ProbeFunc func(ctx context.Context) error

// Ready calls f.
func (f ProbeFunc) Ready(ctx context.Context) error { return f(ctx) }

// Server bundles the ops endpoints. Construct with New, run with Run,
// and use Handler() for in-process testing/embedding without binding.
type Server struct {
	addr  string
	reg   *prometheus.Registry
	probe ReadinessProbe

	// boundAddr is set once Run binds the listener. addrReady is closed
	// at the same moment; Addr() blocks on it. listenErr captures the
	// bind failure so Addr() can surface it instead of returning "".
	boundAddr string
	listenErr error
	addrReady chan struct{}
}

// New returns a Server that will listen on addr (e.g. ":9090"), serve
// /metrics from reg, and use probe for /readyz. The listener is bound
// inside Run so construction is cheap and cannot fail.
func New(addr string, reg *prometheus.Registry, probe ReadinessProbe) *Server {
	return &Server{
		addr:      addr,
		reg:       reg,
		probe:     probe,
		addrReady: make(chan struct{}),
	}
}

// Handler returns the server's HTTP handler tree without binding to a
// listening socket. Useful for testing and for embedding the endpoints
// in a larger mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	return mux
}

// Addr blocks until Run binds the listener (or its bind fails), then
// returns the actual bound address (e.g. "127.0.0.1:33445" when New was
// called with ":0"). Returns the listen error if Run failed to bind, or
// ctx.Err() if ctx is canceled before Run reaches the listen step.
func (s *Server) Addr(ctx context.Context) (string, error) {
	select {
	case <-s.addrReady:
		if s.listenErr != nil {
			return "", s.listenErr
		}
		return s.boundAddr, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Run binds the listener, serves the ops endpoints, and blocks until ctx
// is canceled or the server errors. Returns nil on clean shutdown via
// ctx cancellation, the listen error on bind failure, or the serve error
// otherwise. Shutdown is graceful with a 5-second timeout.
//
// Run must be called at most once per Server instance — addrReady is
// closed during the call, and a second invocation would panic.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.listenErr = fmt.Errorf("listen %s: %w", s.addr, err)
		close(s.addrReady)
		return s.listenErr
	}
	s.boundAddr = ln.Addr().String()
	close(s.addrReady)

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.probe.Ready(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, "not ready: %v", err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}
