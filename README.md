# pgrelay

> Drop-in transactional outbox for Postgres. Producer in any language, dispatcher in Go, traces in OpenTelemetry. No CDC, no Kafka Connect, no schema registry — just a table and a binary.

## Status

🚧 **Under construction.** Working toward `v0.1.0-alpha` (HTTP sink + lease-based claim + integration tests on PG 14–17).

## What it does

A small Go binary that polls a Postgres outbox table and dispatches rows to HTTP or Kafka, with **end-to-end W3C trace context propagation** — so a span started in your Python service stays connected to the span on your Go consumer, across the async hop.

The wedge: polyglot producers (any SQL client) + first-class OpenTelemetry, without the operational weight of Debezium/CDC.

## Roadmap

- `v0.1.0-alpha` — HTTP sink, lease-based claim, retry/DLQ, PG 14–17 integration tests
- `v0.2.0` — Kafka sink (franz-go), `pkg/tracectx` extract/inject helpers, embeddable Go API
- `v0.3.0` — polyglot examples (Go + Python producers, mixed consumer), full docker-compose demo, Tempo trace screenshot
- `v1.0.0` — k6 benchmarks, Grafana dashboard, multi-arch Docker image, launch

## License

[Apache-2.0](./LICENSE)
