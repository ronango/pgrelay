# syntax=docker/dockerfile:1.7

# --- Build stage ---
FROM golang:1.26-alpine@sha256:7a3e50096189ad57c9f9f865e7e4aa8585ed1585248513dc5cda498e2f41812c AS builder

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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639

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
