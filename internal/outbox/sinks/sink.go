// Package sinks defines the abstraction the outbox dispatcher uses to
// deliver events. Each Sink implementation (HTTP now, Kafka in v0.2.0)
// sends a Message; classification of failures into retryable vs terminal
// lets the dispatcher (#6) drive its retry/dead-letter policy without
// knowing transport-specific details.
package sinks

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Canonical sink names returned by Name() implementations. These values
// match the outbox table's CHECK constraint
// (migrations/0001_outbox.up.sql) — keep in sync.
const (
	SinkHTTP  = "http"
	SinkKafka = "kafka"
)

// Sink delivers an outbox Message to its destination.
type Sink interface {
	// Send delivers msg or returns an error. A *RetryableError signals
	// the dispatcher to schedule a retry; any other error is terminal
	// and promotes the row to dead.
	Send(ctx context.Context, msg Message) error

	// Name identifies the sink for metric labels. Returns one of the
	// canonical values declared in this package (SinkHTTP, SinkKafka).
	Name() string
}

// Message is a single outbox row prepared for dispatch. Field set
// mirrors the columns the dispatcher needs to expose at the wire layer:
// IDs become headers/keys, payload becomes the body, trace context
// becomes W3C headers.
type Message struct {
	// ID is the outbox row id, used as the Idempotency-Key on the wire
	// so consumers can dedupe redelivered events.
	ID int64

	// Aggregate identifiers from the outbox row. AggregateID also serves
	// as the canonical partition key for the Kafka sink (v0.2.0).
	AggregateType string
	AggregateID   string
	EventType     string

	// Destination is sink-specific: URL for HTTP, topic for Kafka.
	// Must be non-empty; sinks return a terminal (non-retryable) error
	// otherwise so the row promotes to dead instead of retry-looping.
	Destination string

	// Payload is the raw JSONB bytes from the outbox row, sent as the
	// body verbatim (no envelope).
	Payload []byte

	// Traceparent and Tracestate carry the W3C trace context propagated
	// from the producer through the outbox row. Sinks emit these as
	// reserved headers on the wire; they are not user-controlled.
	Traceparent string
	Tracestate  string

	// Headers carry user-supplied headers from the outbox.headers JSONB
	// column. Sinks merge these with reserved headers (Content-Type,
	// Idempotency-Key, traceparent, tracestate); on conflict, the sink's
	// reserved header wins so user data can never overwrite the wire
	// contract. The HTTP sink compares header names case-insensitively
	// (per RFC 7230); the Kafka sink keeps them case-sensitive.
	Headers map[string]string
}

// RetryableError marks a transport failure the dispatcher should retry.
// RetryAfter > 0 (e.g. server returned a Retry-After header) overrides
// the dispatcher's computed exponential backoff for that one attempt.
//
// HTTP-date variants of the Retry-After header must be converted to a
// duration by the sink before constructing the error.
type RetryableError struct {
	Cause      error
	RetryAfter time.Duration
}

// NewRetryableError wraps cause as a *RetryableError. retryAfter < 0 is
// clamped to 0 so callers don't need a guard before calling.
func NewRetryableError(cause error, retryAfter time.Duration) *RetryableError {
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &RetryableError{Cause: cause, RetryAfter: retryAfter}
}

// Error implements error.
func (e *RetryableError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("retryable (after %s): %v", e.RetryAfter, e.Cause)
	}
	return fmt.Sprintf("retryable: %v", e.Cause)
}

// Unwrap exposes the underlying cause for errors.Is/As.
func (e *RetryableError) Unwrap() error { return e.Cause }

// IsRetryable reports whether err (or any error it wraps) is a
// *RetryableError. Boolean-only path for callers that don't need
// RetryAfter; use AsRetryable when you do.
func IsRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}

// AsRetryable returns the *RetryableError when err (or any error it
// wraps) is one. The dispatcher uses this to read RetryAfter without
// a second errors.As call.
func AsRetryable(err error) (*RetryableError, bool) {
	var re *RetryableError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}
