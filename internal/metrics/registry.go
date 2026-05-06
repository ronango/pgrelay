// Package metrics provides the shared Prometheus registry used by the
// dispatcher and its supporting subsystems. The registry is pre-loaded
// with Go runtime and process collectors so /metrics exposes them
// alongside pgrelay's domain metrics (registered by callers such as
// internal/pgconn).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewRegistry returns a fresh prometheus.Registry with the Go runtime
// collector and process collector pre-registered. Callers register their
// own metrics on the same registry — there is one registry per pgrelay
// process.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		// Exposes go_build_info{path,version,checksum} so operators can
		// confirm which binary is serving /metrics.
		collectors.NewBuildInfoCollector(),
	)
	return reg
}
