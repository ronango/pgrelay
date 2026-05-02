# syntax=docker/dockerfile:1.7

# --- Build stage ---
FROM golang:1.23-alpine@sha256:383395b794dffa5b53012a212365d40c8e37109a626ca30d6151c8348d380b5f AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=""

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
    -o /out/pgrelay \
    ./cmd/pgrelay

# --- Final stage ---
FROM gcr.io/distroless/static-debian12:nonroot@sha256:a9329520abc449e3b14d5bc3a6ffae065bdde0f02667fa10880c49b35c109fd1

ARG VERSION=dev
ARG COMMIT=""

LABEL org.opencontainers.image.title="pgrelay" \
      org.opencontainers.image.description="Postgres-native transactional outbox dispatcher with end-to-end OpenTelemetry tracing" \
      org.opencontainers.image.source="https://github.com/ronango/pgrelay" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=builder /out/pgrelay /pgrelay

USER nonroot:nonroot

ENTRYPOINT ["/pgrelay"]
