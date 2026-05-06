package metrics_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ronango/pgrelay/internal/metrics"
)

// TestNewRegistry_HasDefaultCollectors gathers from a fresh registry and
// asserts the expected metric families appear. Process collector reads
// /proc, so process_* is asserted only on Linux (CI runners + container
// runtime); go_* and go_build_info ship cross-platform.
func TestNewRegistry_HasDefaultCollectors(t *testing.T) {
	reg := metrics.NewRegistry()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	var sawGo, sawProcess, sawBuildInfo bool
	for _, mf := range mfs {
		name := mf.GetName()
		switch {
		case name == "go_build_info":
			sawBuildInfo = true
		case strings.HasPrefix(name, "go_"):
			sawGo = true
		case strings.HasPrefix(name, "process_"):
			sawProcess = true
		}
	}
	if !sawGo {
		t.Error("expected go_* metrics from runtime collector")
	}
	if !sawBuildInfo {
		t.Error("expected go_build_info from BuildInfoCollector")
	}
	if runtime.GOOS == "linux" && !sawProcess {
		t.Error("expected process_* metrics from process collector on linux")
	}
}

// TestNewRegistry_IndependentInstances confirms each call returns a fresh
// registry — no shared global state, so tests/components can build their
// own without conflicting on metric name registration.
func TestNewRegistry_IndependentInstances(t *testing.T) {
	a := metrics.NewRegistry()
	b := metrics.NewRegistry()
	if a == b {
		t.Error("NewRegistry returned the same registry twice")
	}
}
