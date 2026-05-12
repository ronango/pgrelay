//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ronango/pgrelay/internal/testdb"
)

// freePort grabs an ephemeral port the kernel just released. There's
// an inherent race between Close and the dispatcher binding it, but
// the loopback window is microsecond-scale and reliable in practice.
// The alternative — parsing the dispatcher's startup log — is fragile.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func seedPending(t *testing.T, pool *pgxpool.Pool, destination string) {
	t.Helper()
	const sql = `
		INSERT INTO pgrelay_outbox (aggregate_type, aggregate_id, event_type, payload, sink, destination)
		VALUES ('order', 'agg-1', 'created', '{"k":"v"}'::jsonb, 'http', $1)
	`
	if _, err := pool.Exec(t.Context(), sql, destination); err != nil {
		t.Fatalf("seed pending row: %v", err)
	}
}

func TestRun_HappyPathDeliversThenShutsDown(t *testing.T) {
	pool := testdb.New(t)
	dsn := pool.Config().ConnString()

	received := make(chan struct{}, 1)
	sinkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sinkSrv.Close)

	seedPending(t, pool, sinkSrv.URL)

	bin := buildBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// CommandContext sends SIGKILL on ctx done; here that's a safety
	// net only — the test exercises SIGTERM via cmd.Process.Signal.
	cmd := exec.CommandContext(ctx, bin, "run")
	cmd.Env = append(envBaseline(),
		"PGRELAY_DATABASE_URL="+dsn,
		"PGRELAY_OPS_ADDR="+freePort(t),
		"PGRELAY_POLL_INTERVAL=50ms",
		"PGRELAY_LEASE_DURATION=2s",
		"PGRELAY_SHUTDOWN_TIMEOUT=3s",
	)

	var stdout, stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pgrelay: %v", err)
	}
	stdoutDone := drainBuffer(&stdout, stdoutPipe)
	stderrDone := drainBuffer(&stderr, stderrPipe)

	select {
	case <-received:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-stdoutDone
		<-stderrDone
		t.Fatalf("sink never received a request; stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	if err := waitWithTimeout(cmd, 10*time.Second); err != nil {
		t.Fatalf("pgrelay run exit: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	<-stdoutDone
	<-stderrDone
}

func TestRun_NonZeroExitOnMissingConfig(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "run")
	cmd.Env = envBaseline() // strip inherited PGRELAY_*
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit when PGRELAY_DATABASE_URL is missing; output=%s", out)
	}
	if !strings.Contains(string(out), "PGRELAY_DATABASE_URL") {
		t.Errorf("error output = %q, want mention of PGRELAY_DATABASE_URL", out)
	}
}

var errWaitTimeout = errors.New("pgrelay did not exit within timeout (killed)")

func waitWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return errWaitTimeout
	}
}
