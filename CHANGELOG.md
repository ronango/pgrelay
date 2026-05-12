# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0-alpha] - 2026-05-13

Initial alpha release. Single-process Postgres-outbox dispatcher with HTTP sink, retry/dead-letter policy, and an OpenTelemetry-aware wire protocol. Integration-tested against PG 14 / 15 / 16 / 17.

### Added

- `pgrelay_outbox` table with partial indexes on `(next_attempt_at)` and `(leased_until)` for the dispatcher's hot paths.
- `pgrelay` binary with three subcommands:
  - `version` — build version, commit, Go runtime
  - `migrate {up,down,status} [--steps N] [--yes]` — `golang-migrate` wrapper; `down` requires explicit `--yes` confirmation
  - `run` — wires pool, dispatcher, and health server; blocks until SIGINT/SIGTERM with bounded graceful shutdown
- HTTP sink: POST `application/json`, classify 408/425/429/5xx as retryable (with `Retry-After` honored as a floor), 3xx as terminal (no redirect-following), reserved-header allowlist for `Content-Type` / `Idempotency-Key` / `traceparent` / `tracestate`.
- Lease-based claim via `FOR UPDATE SKIP LOCKED` + CTE wrapper for deterministic RETURNING order.
- Exponential backoff with jitter; pluggable RNG for deterministic tests.
- Lease sweeper reclaims orphaned `in_flight` rows whose `leased_until` has expired.
- Input validation: rejects HTTP header control bytes (CR, LF, NUL, DEL, and other ASCII controls), sanitizes `last_error` (4 KiB cap, control-byte strip, UTF-8 safe with no mid-rune truncation), drops rows whose payload exceeds 1 MiB.
- W3C trace context propagation through `traceparent` / `tracestate` headers.
- OpenTelemetry SDK setup with OTLP/HTTP exporter and configurable sampler.
- Prometheus surface: `pgrelay_outbox_attempts_total{result}`, `pgrelay_outbox_dispatch_duration_seconds{sink}`, `pgrelay_outbox_orphans_reclaimed_total`, `pgrelay_outbox_backlog_seconds`, `pgrelay_outbox_rows{status}`, plus `pgrelay_db_query_*` and `pgrelay_db_pool_*` from the pgx tracer.
- Health endpoints (`/healthz`, `/readyz`) and Prometheus scrape (`/metrics`) on configurable `PGRELAY_OPS_ADDR`.
- Graceful shutdown bounded by `PGRELAY_SHUTDOWN_TIMEOUT` (default 30s); non-zero exit on timeout via `ErrShutdownTimeout`.
- Integration tests across PG 14 / 15 / 16 / 17 via `testcontainers-go`.
- Distroless-nonroot Dockerfile suitable for multi-arch build.

### Known Limitations

- Dispatcher emits no OpenTelemetry spans of its own — only header-level propagation. Dispatcher spans land in v0.2.0.
- Per-aggregate ordering is not guaranteed under concurrent producers because `BIGSERIAL` is monotonic per connection, not per commit. Global ordering by `next_attempt_at` is preserved.
- Payload size guard is app-layer only; DB-level `CHECK` constraint deferred to a v0.2 migration.
- `internal/outbox` couples to `internal/config` for dispatcher configuration. An embeddable Go API (`pkg/pgrelay`) and the corresponding decoupling are planned for v0.2.0.
- Kafka sink, polyglot examples (Go + Python producers), and benchmarks are roadmap items — see [README](./README.md#roadmap).

[0.1.0-alpha]: https://github.com/ronango/pgrelay/releases/tag/v0.1.0-alpha
