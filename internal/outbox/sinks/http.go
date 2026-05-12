package sinks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPSink delivers messages via HTTP POST. It classifies failures into
// retryable (408 / 425 / 429 / 5xx + network errors) and terminal
// (everything else, including malformed destinations and 3xx); the
// dispatcher (#6) owns the retry policy and surfaces metrics.
//
// Trust boundary: any caller with INSERT on pgrelay_outbox controls
// row.destination, and pgrelay will POST to that URL from the
// dispatcher's network — restrict INSERT to trusted producers or this
// becomes an SSRF pivot inside the cluster.
type HTTPSink struct {
	client *http.Client
}

// HTTPSinkOption configures an HTTPSink.
type HTTPSinkOption func(*HTTPSink)

// Bounds applied while reading response bodies — cap memory for both
// keepalive draining and error-context snippets.
const (
	maxResponseBytes = 64 * 1024 // hard cap on body read; bigger responses end the connection
	maxErrSnippet    = 512       // first N bytes included in terminal/retryable error strings
)

// NewHTTPSink returns an HTTPSink with sensible production defaults:
// 30-second per-request timeout; transport cloned from http.DefaultTransport
// (so TLSHandshakeTimeout, ExpectContinueTimeout, dial keepalive remain
// in place) with pool fields tuned for low-cardinality destination sets;
// redirects disabled — a 3xx response is surfaced as terminal, since
// silently re-POSTing the Idempotency-Key to a different host would
// surprise operators.
//
// Override the client via WithHTTPClient for tests or for tuning at
// the application level.
func NewHTTPSink(opts ...HTTPSinkOption) *HTTPSink {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxConnsPerHost = 10
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second

	s := &HTTPSink{
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithHTTPClient overrides the default *http.Client. Tests use this to
// route through httptest.Server; production callers use it to share a
// pre-configured outer client.
func WithHTTPClient(c *http.Client) HTTPSinkOption {
	return func(s *HTTPSink) { s.client = c }
}

// Name returns SinkHTTP.
func (s *HTTPSink) Name() string { return SinkHTTP }

// reservedHTTPHeaders are owned by the sink and cannot be set via
// Message.Headers. Stored in canonical form for case-insensitive lookup
// against http.CanonicalHeaderKey-normalized user input.
var reservedHTTPHeaders = map[string]struct{}{
	http.CanonicalHeaderKey("Content-Type"):    {},
	http.CanonicalHeaderKey("Idempotency-Key"): {},
	http.CanonicalHeaderKey("Traceparent"):     {},
	http.CanonicalHeaderKey("Tracestate"):      {},
}

// Send POSTs msg.Payload to msg.Destination as application/json. Returns
// nil on 2xx, *RetryableError on 408/425/429/5xx + network errors (with
// Retry-After surfaced when the server provides one), ctx.Err on caller
// cancellation, or a terminal error on malformed input / 3xx / 4xx.
func (s *HTTPSink) Send(ctx context.Context, msg Message) error {
	if msg.Destination == "" {
		return errors.New("destination required")
	}
	if msg.Traceparent != "" && !validHeaderValue(msg.Traceparent) {
		return errors.New("traceparent contains control character")
	}
	if msg.Tracestate != "" && !validHeaderValue(msg.Tracestate) {
		return errors.New("tracestate contains control character")
	}
	for k, v := range msg.Headers {
		if !validHeaderName(k) {
			return fmt.Errorf("header name %q invalid (RFC 7230 tchar)", k)
		}
		if !validHeaderValue(v) {
			return fmt.Errorf("header %q value contains control character", k)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msg.Destination, bytes.NewReader(msg.Payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	// Reserved headers — set first so user headers can't reach them.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", strconv.FormatInt(msg.ID, 10))
	if msg.Traceparent != "" {
		req.Header.Set("traceparent", msg.Traceparent)
	}
	if msg.Tracestate != "" {
		req.Header.Set("tracestate", msg.Tracestate)
	}

	for k, v := range msg.Headers {
		if _, isReserved := reservedHTTPHeaders[http.CanonicalHeaderKey(k)]; isReserved {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		// Caller-canceled context: propagate as ctx.Err so the dispatcher
		// can distinguish shutdown from a transient transport problem.
		if errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		// All other transport errors (DNS, refused, timeout, conn reset)
		// are transient; the dispatcher applies its own backoff.
		return NewRetryableError(fmt.Errorf("http do: %w", err), 0)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	if isSuccess(resp.StatusCode) {
		return nil
	}

	if isRetryableStatus(resp.StatusCode) {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return NewRetryableError(formatHTTPError(resp.StatusCode, body), retryAfter)
	}

	return formatHTTPError(resp.StatusCode, body)
}

// formatHTTPError builds an error containing the status code, reason
// phrase, and a bounded body snippet to give operators something useful
// in logs and dead-letter inspection.
func formatHTTPError(code int, body []byte) error {
	snippet := bytes.TrimSpace(body)
	if len(snippet) > maxErrSnippet {
		snippet = snippet[:maxErrSnippet]
	}
	if len(snippet) == 0 {
		return fmt.Errorf("http %d %s", code, http.StatusText(code))
	}
	return fmt.Errorf("http %d %s: %s", code, http.StatusText(code), snippet)
}

func isSuccess(status int) bool { return status >= 200 && status < 300 }

// isRetryableStatus per the issue: 408, 425, 429, 5xx.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, // 408
		http.StatusTooEarly,        // 425
		http.StatusTooManyRequests: // 429
		return true
	}
	return status >= 500 && status < 600
}

// validHeaderValue mirrors net/textproto's check: rejects any control
// byte below 0x20 (except tab) and DEL (0x7f). Without this bail-early
// the row would be retried until MaxAttempts on transport-classified
// errors. OWS around values is not stripped — Go's net/http passes them
// through; producers must trim if downstream peers are strict.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// validHeaderName rejects characters HTTP forbids in header names per
// RFC 7230 §3.2.6 (control bytes, separators, and whitespace).
func validHeaderName(name string) bool {
	// Empty must be rejected explicitly — the loop alone would pass it.
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		// tchar set: alphanumerics + !#$%&'*+-.^_`|~
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// parseRetryAfter returns the duration encoded in a Retry-After header.
// Supports the two RFC 7231 forms: delta-seconds (e.g. "120") and
// HTTP-date (e.g. "Wed, 21 Oct 2015 07:28:00 GMT"). Returns 0 for empty,
// negative, past-dated, or unparseable values; the dispatcher then falls
// back to its own exponential backoff.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
