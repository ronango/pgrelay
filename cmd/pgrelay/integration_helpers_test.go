//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// One binary for the whole package — go build of the same source
// produces an identical artifact, and a single compile keeps the
// suite under ~60s on a cold runner. The dir lives through TestMain
// so subsequent tests don't get a stale path after the first test's
// t.TempDir cleanup.
var (
	binOnce sync.Once
	binDir  string
	binPath string
	binErr  error
)

// TestMain owns the build directory's lifetime. Without this, the
// `t.TempDir()` from the first caller of buildBinary would be deleted
// at the end of that test and subsequent tests would exec a missing
// path.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pgrelay-bin-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: create build dir:", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func buildBinary(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		binPath = filepath.Join(binDir, "pgrelay")
		out, err := exec.Command("go", "build", "-o", binPath, "./").CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("%s: %w", string(out), err)
		}
	})
	if binErr != nil {
		t.Fatalf("build pgrelay binary: %v", binErr)
	}
	return binPath
}

// envBaseline returns a minimal env (PATH, HOME, TMPDIR) with every
// PGRELAY_* stripped so a developer's local .env doesn't bleed into
// the subprocess and mask assertion failures. Shared by every test
// that invokes the binary.
func envBaseline() []string {
	keep := make([]string, 0, 3)
	for _, k := range []string{"PATH", "TMPDIR", "HOME"} {
		if v := os.Getenv(k); v != "" {
			keep = append(keep, k+"="+v)
		}
	}
	return keep
}

type cmdResult struct {
	stdout string
	stderr string
	err    error
}

// runArgsTimeout caps a one-shot subcommand. A hung migrate (e.g. an
// unreachable DSN that blocks on TCP connect) is bounded here rather
// than waiting for `go test -timeout`.
const runArgsTimeout = 30 * time.Second

// runArgs runs the built binary once and captures stdout + stderr.
// One-shot — for `version` and `migrate`. The `run` subcommand has
// its own helper that wires SIGTERM and timeouts.
func runArgs(t testing.TB, envOverrides []string, args ...string) cmdResult {
	t.Helper()
	// Resolve the binary outside the timeout — the first caller pays
	// the `go build -race` cost (~30s cold), and that's compile time,
	// not subcommand runtime.
	bin := buildBinary(t)

	ctx, cancel := context.WithTimeout(t.Context(), runArgsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(envBaseline(), envOverrides...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return cmdResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// drainBuffer pumps r into buf in a goroutine. EOF fires when *exec.Cmd
// closes the pipe after Wait, so the returned done channel signals
// "subprocess output fully captured". Callers must read done before
// returning to keep the goroutine bounded by the test's lifetime.
func drainBuffer(buf *bytes.Buffer, r io.Reader) chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(buf, r)
		close(done)
	}()
	return done
}
