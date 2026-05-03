# syntax=docker/dockerfile:1.7

# --- Build stage ---
FROM golang:1.26-alpine@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder

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
