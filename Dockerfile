# syntax=docker/dockerfile:1.7

# --- Build stage ---
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder

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
