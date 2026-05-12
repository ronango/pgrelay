# pgrelay

> Drop-in transactional outbox for Postgres. Producer in any language, dispatcher in Go, traces in OpenTelemetry. No CDC, no Kafka Connect, no schema registry — just a table and a binary.

## Status

`v0.1.0-alpha` — HTTP sink, lease-based claim with `FOR UPDATE SKIP LOCKED`, exponential backoff with jitter, lease sweeper for orphaned rows, Prometheus surface, W3C trace context propagation, graceful shutdown. Integration-tested against PG 14 / 15 / 16 / 17.

## What it does

A small Go binary that polls a Postgres outbox table and dispatches rows to HTTP (Kafka in v0.2), with **end-to-end W3C trace context propagation** — so a span started in your Python service stays connected to the span on your Go consumer, across the async hop.

The wedge: polyglot producers (any SQL client) + first-class OpenTelemetry, without the operational weight of Debezium/CDC.

## 60-second quick start

Requires Docker (or Podman with the compose plugin).

```sh
# 1. Start Postgres.
docker compose up -d postgres

# 2. Apply migrations.
docker compose run --rm pgrelay migrate up

# 3. Seed an outbox row pointed at any HTTP receiver
# (replace example.invalid with your own URL to see deliveries).
docker compose exec -T postgres psql -U pgrelay -d pgrelay <<'SQL'
INSERT INTO pgrelay_outbox (aggregate_type, aggregate_id, event_type, payload, sink, destination)
VALUES ('order', 'order-1', 'created', '{"id":"order-1"}'::jsonb, 'http', 'https://example.invalid/replace-me');
SQL

# 4. Run the dispatcher.
docker compose up pgrelay
```

Health and metrics are exposed at `http://localhost:9090/{healthz,readyz,metrics}`.
Configuration is via env vars — see [`internal/config/config.go`](./internal/config/config.go) for the full list.

## Roadmap

- `v0.2.0` — Kafka sink (franz-go), `pkg/tracectx` extract/inject helpers, embeddable Go API, dispatcher spans
- `v0.3.0` — polyglot examples (Go + Python producers, mixed consumer), full docker-compose demo, Tempo trace screenshot
- `v1.0.0` — k6 benchmarks, Grafana dashboard, multi-arch Docker image, launch

## License

[Apache-2.0](./LICENSE)
